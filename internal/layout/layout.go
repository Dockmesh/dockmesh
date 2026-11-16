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
	// CheckoutDir is the source run directory the server uses without
	// config (see CheckoutDir); when no candidate exists, Migrate writes a
	// config naming it as the data dir.
	CheckoutDir string
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

// CheckoutDir returns workDir when the server runs without any config and
// workDir holds the database of a source run: up to 0.5.x a server without
// config kept ./data and ./stacks below its working directory, so that
// directory stays its data dir. envFiles are the env files to look for
// besides ConfigFile and LegacyEnvFiles; any of them present, or
// DOCKMESH_DATA_DIR or DOCKMESH_DB_PATH set, is config and wins. Empty
// when workDir is not such a directory; lstat defaults to os.Lstat.
func CheckoutDir(workDir string, getenv func(string) string, envFiles []string, lstat func(string) (fs.FileInfo, error)) string {
	if lstat == nil {
		lstat = os.Lstat
	}
	if !filepath.IsAbs(workDir) || getenv("DOCKMESH_DATA_DIR") != "" || getenv("DOCKMESH_DB_PATH") != "" {
		return ""
	}
	for _, f := range append(append([]string{ConfigFile}, LegacyEnvFiles...), envFiles...) {
		if f == "" {
			continue
		}
		if _, err := lstat(f); !errors.Is(err, fs.ErrNotExist) {
			return ""
		}
	}
	workDir = filepath.Clean(workDir)
	if workDir == config.DefaultDataDir {
		return ""
	}
	fi, err := lstat(dbPathBelow(workDir))
	if err != nil || fi.IsDir() {
		return ""
	}
	return workDir
}

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
