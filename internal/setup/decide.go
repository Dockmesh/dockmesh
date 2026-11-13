package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
)

// Mode is what an install is, as far as setup is concerned. The zero
// value is SetUp, so a forgotten or failed check never opens setup mode.
type Mode int

const (
	// SetUp never opens setup mode.
	SetUp Mode = iota
	// LegacyUnfinished is an install up to 0.5.x whose wizard never
	// finished: an env file with DOCKMESH_SETUP_FORCE=true and no account.
	LegacyUnfinished
	// NeverSetUp has no config, no database, no data and no account.
	NeverSetUp
)

func (m Mode) String() string {
	switch m {
	case LegacyUnfinished:
		return "legacy unfinished"
	case NeverSetUp:
		return "never set up"
	default:
		return "set up"
	}
}

// Facts are the inputs of Decide.
type Facts struct {
	// EnvFile: an env file exists at any location.
	EnvFile bool
	// Config: EnvFile, or DOCKMESH_DB_PATH set.
	Config bool
	// Force is DOCKMESH_SETUP_FORCE=true.
	Force bool
	// DB: the database the server uses exists.
	DB bool
	// ForeignDB: a database exists at a known location other than the
	// one the server uses; its accounts are not counted, so it counts as
	// an install with data.
	ForeignDB bool
	// Data: files an install leaves next to its database (secrets, keys,
	// the agent CA, stacks) exist.
	Data bool
	// Users in the server's database; negative when unknown.
	Users int
}

// Decide maps Facts to a Mode. Anything it cannot place is SetUp: wrongly
// opening setup mode exposes an install with data, wrongly closing it is
// recovered with `dockmesh admin create`.
func Decide(f Facts) Mode {
	switch {
	case f.Users != 0, f.ForeignDB:
		return SetUp
	case !f.Config && !f.DB && !f.Data:
		return NeverSetUp
	case f.EnvFile && f.Force:
		return LegacyUnfinished
	default:
		return SetUp
	}
}

// Probe lists where an install may keep its config and data.
type Probe struct {
	// ConfigFiles are extra env files to look for: --env-file and the one
	// the service unit names. layout.ConfigFile and layout.LegacyEnvFiles
	// are always checked.
	ConfigFiles []string
	// DBPathSet is DOCKMESH_DB_PATH being set.
	DBPathSet bool
	Force     bool
	// Paths of the server; layout.KnownDBPaths are checked as well.
	Paths config.Paths
	// Lstat and ReadDir default to the os functions; tests root the
	// paths elsewhere.
	Lstat   func(string) (fs.FileInfo, error)
	ReadDir func(string) ([]fs.DirEntry, error)
}

// Scan checks the file system. The server runs it before config.Load and
// db.Open, which create the secrets file and the database a fresh install
// lacks. Users is left unknown for the caller to fill in.
func (p Probe) Scan() Facts {
	lstat, readDir := p.Lstat, p.ReadDir
	if lstat == nil {
		lstat = os.Lstat
	}
	if readDir == nil {
		readDir = os.ReadDir
	}
	f := Facts{Force: p.Force, Users: -1}
	configs := append([]string{layout.ConfigFile}, layout.LegacyEnvFiles...)
	for _, c := range append(configs, p.ConfigFiles...) {
		if c != "" && present(lstat, c) {
			f.EnvFile = true
		}
	}
	f.Config = f.EnvFile || p.DBPathSet

	db := p.Paths.DBPath
	active, err := lstat(db)
	switch {
	case err == nil:
		f.DB = true
	case !isNotExist(err):
		f.DB = true
		active = nil
	}
	for _, known := range layout.KnownDBPaths() {
		if filepath.Clean(known) == filepath.Clean(db) {
			continue
		}
		fi, err := lstat(known)
		if isNotExist(err) {
			continue
		}
		if err != nil || active == nil || !os.SameFile(fi, active) {
			f.ForeignDB = true
		}
	}

	dataDir := filepath.Dir(db)
	data := []string{
		db + "-wal",
		p.Paths.SecretsPath,
		p.Paths.SecretsKeyPath,
		p.Paths.AuditGenesisPath,
		filepath.Join(dataDir, "agents-ca.key"),
		filepath.Join(dataDir, "agents-ca.crt"),
	}
	for _, d := range data {
		if d != "" && present(lstat, d) {
			f.Data = true
		}
	}
	if p.Paths.StacksRoot != "" {
		entries, err := readDir(p.Paths.StacksRoot)
		if len(entries) > 0 || (err != nil && !isNotExist(err)) {
			f.Data = true
		}
	}
	return f
}

// present counts any answer but "does not exist" as a file being there:
// a config the service may not read is still a config, and a symlink whose
// target is gone may point at a disk that is not mounted yet.
func present(lstat func(string) (fs.FileInfo, error), path string) bool {
	_, err := lstat(path)
	return !isNotExist(err)
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
