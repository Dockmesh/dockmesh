package config

import "path/filepath"

// DefaultDataDir is the data directory when neither DOCKMESH_DATA_DIR nor
// DOCKMESH_DB_PATH names one. It matches the installer's default.
const DefaultDataDir = "/var/lib/dockmesh"

// Paths are the on-disk locations of a server install.
type Paths struct {
	DataDir          string
	DBPath           string
	StacksRoot       string
	SecretsPath      string
	AuditGenesisPath string
	SecretsKeyPath   string
	UpdateDir        string
}

// ResolvePaths derives every path from DOCKMESH_DATA_DIR. The per-path
// variables override single paths; installs from before DOCKMESH_DATA_DIR
// set all of them, so their data stays where it is.
func ResolvePaths(getenv func(string) string) Paths {
	dir := getenv("DOCKMESH_DATA_DIR")
	if dir == "" {
		dir = InferDataDir(getenv("DOCKMESH_DB_PATH"))
	}
	if dir == "" {
		dir = DefaultDataDir
	}
	or := func(key string, elem ...string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return filepath.Join(append([]string{dir}, elem...)...)
	}
	return Paths{
		DataDir:          dir,
		DBPath:           or("DOCKMESH_DB_PATH", "data", "dockmesh.db"),
		StacksRoot:       or("DOCKMESH_STACKS_ROOT", "stacks"),
		SecretsPath:      or("DOCKMESH_SECRETS_PATH", "data", "secrets.env"),
		AuditGenesisPath: or("DOCKMESH_AUDIT_GENESIS_PATH", "data", "audit-genesis.sha256"),
		SecretsKeyPath:   or("DOCKMESH_SECRETS_KEY_PATH", "data", "secrets.age-key"),
		UpdateDir:        filepath.Join(dir, "update"),
	}
}

// InferDataDir returns the data directory of an install configured only
// through DOCKMESH_DB_PATH: the parent of <dir>/data/dockmesh.db, else the
// directory holding the database. Empty for an empty dbPath.
func InferDataDir(dbPath string) string {
	if dbPath == "" {
		return ""
	}
	dir := filepath.Dir(dbPath)
	if filepath.Base(dir) == "data" {
		return filepath.Dir(dir)
	}
	return dir
}
