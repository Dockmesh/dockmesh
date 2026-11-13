// Package layout knows where a server install keeps its config, and moves
// the config of an install from before /etc/dockmesh to that place. The
// installer and later the root updater call Migrate through
// `dockmesh system migrate-layout`.
package layout

import (
	"errors"
	"strings"
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
