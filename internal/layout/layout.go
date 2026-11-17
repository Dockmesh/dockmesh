// Package layout knows where a server install keeps its config, and moves
// the config of an install from before /etc/dockmesh to that place. The
// installer calls Migrate through `dockmesh system migrate-layout`.
package layout

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/config"
)

const (
	// ConfigDir holds the server config: root:dockmesh, 0750.
	ConfigDir = "/etc/dockmesh"
	// ConfigFile is the server config, root:dockmesh, 0640.
	ConfigFile = ConfigDir + "/" + envFileName

	envFileName = "dockmesh.env"
)

// LegacyEnvFiles are where install.sh and `dockmesh init` put dockmesh.env
// before ConfigFile: the Linux default, /data for hosts with a separate
// data volume, and the macOS default.
var LegacyEnvFiles = []string{
	"/var/lib/dockmesh/dockmesh.env",
	"/data/dockmesh.env",
	"/usr/local/var/dockmesh/dockmesh.env",
}

// MigrateOptions configure Migrate. The zero values of UID and GID are
// root, which is what the installer runs as.
type MigrateOptions struct {
	// ConfigDir defaults to ConfigDir; tests point it elsewhere.
	ConfigDir string
	// Candidates are the env files to move, first match wins. The
	// caller puts the one the service unit names first.
	Candidates []string
	// UID and GID own the config dir and file.
	UID, GID int
	// CheckoutDir names the directory of a source run without config
	// (see CheckoutProbe); Migrate writes a config with it as the data
	// dir, and refuses when a candidate exists.
	CheckoutDir string
	// trustTop bounds the directories TrustedCheckout checks; tests set
	// it to their temp dir.
	trustTop string
}

// Outcome says what Migrate did.
type Outcome int

const (
	// Moved: the config moved to ConfigFile, a symlink is at the old place.
	Moved Outcome = iota
	// AlreadyMigrated: ConfigFile exists; nothing was touched.
	AlreadyMigrated
	// NothingFound: no env file at any candidate path.
	NothingFound
	// SetupPending: the env file still carries DOCKMESH_SETUP_FORCE, which
	// the setup wizard removes; the file stays until then because the
	// service cannot rewrite ConfigFile.
	SetupPending
	// CheckoutConfigured: ConfigFile was written with DOCKMESH_DATA_DIR
	// naming MigrateOptions.CheckoutDir.
	CheckoutConfigured
)

// Result reports a Migrate run.
type Result struct {
	Outcome Outcome
	// From is the env file that was moved or left in place.
	From string
	// DataDir is the DOCKMESH_DATA_DIR the moved config names.
	DataDir string
}

// ErrUntrusted marks a source env file Migrate refuses to move.
var ErrUntrusted = errors.New("untrusted env file")

// maxEnvFileSize bounds what Migrate reads; a real config is a few hundred bytes.
const maxEnvFileSize = 1 << 20

// parseEnv returns the KEY=VALUE pairs of an env file. A key set twice
// keeps its last value, as systemd's EnvironmentFile= does.
func parseEnv(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// KnownDBPaths are where an install keeps its database when only the data
// dir is configured: below the default data dir, below every directory of
// LegacyEnvFiles and, for a source run up to 0.5.x, below workDir.
func KnownDBPaths(workDir string) []string {
	dirs := []string{config.DefaultDataDir}
	for _, f := range LegacyEnvFiles {
		dirs = append(dirs, filepath.Dir(f))
	}
	if workDir != "" {
		dirs = append(dirs, workDir)
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		p := dbPathBelow(d)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// CheckoutProbe decides whether the server runs on the data of a source
// run: up to 0.5.x a server without config kept ./data and ./stacks below
// its working directory, so that directory stays its data dir.
type CheckoutProbe struct {
	WorkDir string
	Getenv  func(string) string
	// EnvFiles are env files to look for besides ConfigFile and
	// LegacyEnvFiles: --env-file and the one the service unit names.
	EnvFiles []string
	// Lstat defaults to os.Lstat; Trust to TrustedCheckout for the euid.
	Lstat func(string) (fs.FileInfo, error)
	Trust func(dir string) error
}

// Dir returns WorkDir when it holds data/dockmesh.db and nothing
// configures the server: no env file, no DOCKMESH_DATA_DIR and no
// DOCKMESH_DB_PATH. The error explains a directory that would qualify
// but fails Trust; Dir is empty then.
func (p CheckoutProbe) Dir() (string, error) {
	lstat, trust := p.Lstat, p.Trust
	if lstat == nil {
		lstat = os.Lstat
	}
	if trust == nil {
		trust = func(dir string) error { return TrustedCheckout(dir, os.Geteuid()) }
	}
	if !filepath.IsAbs(p.WorkDir) || p.Getenv("DOCKMESH_DATA_DIR") != "" || p.Getenv("DOCKMESH_DB_PATH") != "" {
		return "", nil
	}
	for _, f := range append(append([]string{ConfigFile}, LegacyEnvFiles...), p.EnvFiles...) {
		if f == "" {
			continue
		}
		if _, err := lstat(f); !errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
	}
	dir := filepath.Clean(p.WorkDir)
	// "/" is the working directory systemd gives every service.
	if dir == "/" || dir == config.DefaultDataDir {
		return "", nil
	}
	if fi, err := lstat(dbPathBelow(dir)); err != nil || fi.IsDir() {
		return "", nil //nolint:nilerr // no database, no source run to keep
	}
	if err := trust(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// HasCheckoutDB reports a file or symlink at workDir/data/dockmesh.db,
// what CheckoutProbe.Dir counts as a database before its trust check. The
// setup decision counts it as an install whether or not TrustedCheckout
// accepts the directory: it can only keep setup closed and is never used
// as data. lstat defaults to os.Lstat.
func HasCheckoutDB(workDir string, lstat func(string) (fs.FileInfo, error)) bool {
	if lstat == nil {
		lstat = os.Lstat
	}
	if !filepath.IsAbs(workDir) {
		return false
	}
	fi, err := lstat(CheckoutDBPath(workDir))
	return err == nil && !fi.IsDir()
}

// CheckoutDBPath is the database of a source run started in workDir.
func CheckoutDBPath(workDir string) string { return dbPathBelow(filepath.Clean(workDir)) }

// dbPathBelow is the database of an install configured only through
// DOCKMESH_DATA_DIR=dataDir.
func dbPathBelow(dataDir string) string {
	return config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" {
			return dataDir
		}
		return ""
	}).DBPath
}
