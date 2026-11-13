package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

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
	// NeverSetUp has no config, no database and no account anywhere.
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
	// Config: an env file at any location, or DOCKMESH_DB_PATH set.
	Config bool
	// Force is DOCKMESH_SETUP_FORCE=true.
	Force bool
	// DB: the database the server uses exists.
	DB bool
	// ForeignDB: a database exists at a known location other than the
	// one the server uses; its accounts are not counted, so it counts as
	// an install with data.
	ForeignDB bool
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
	case !f.Config && !f.DB:
		return NeverSetUp
	case f.Config && f.Force:
		return LegacyUnfinished
	default:
		return SetUp
	}
}

// Probe lists where an install may keep its config and database.
type Probe struct {
	// ConfigFiles are extra env files to look for: --env-file and the one
	// the service unit names. layout.ConfigFile and layout.LegacyEnvFiles
	// are always checked.
	ConfigFiles []string
	// DBPathSet is DOCKMESH_DB_PATH being set.
	DBPathSet bool
	Force     bool
	// DBPath is the database the server uses; layout.KnownDBPaths are
	// checked as well.
	DBPath string
	// Stat defaults to os.Stat; tests root the paths elsewhere.
	Stat func(string) (fs.FileInfo, error)
}

// Scan checks the file system. The server runs it before it opens, and so
// creates, its database. Users is left unknown for the caller to fill in.
func (p Probe) Scan() Facts {
	stat := p.Stat
	if stat == nil {
		stat = os.Stat
	}
	f := Facts{Force: p.Force, Config: p.DBPathSet, Users: -1}
	configs := append([]string{layout.ConfigFile}, layout.LegacyEnvFiles...)
	for _, c := range append(configs, p.ConfigFiles...) {
		if c != "" && present(stat, c) {
			f.Config = true
		}
	}
	active, err := stat(p.DBPath)
	switch {
	case err == nil:
		f.DB = true
	case !isNotExist(err):
		f.DB = true
		active = nil
	}
	for _, known := range layout.KnownDBPaths() {
		if filepath.Clean(known) == filepath.Clean(p.DBPath) {
			continue
		}
		fi, err := stat(known)
		if isNotExist(err) {
			continue
		}
		if err != nil || active == nil || !os.SameFile(fi, active) {
			f.ForeignDB = true
		}
	}
	return f
}

// present counts any answer but "does not exist" as a file being there:
// a config the service may not read is still a config.
func present(stat func(string) (fs.FileInfo, error), path string) bool {
	_, err := stat(path)
	return !isNotExist(err)
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
