package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/restore"
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
	// Lstat, Stat and ReadDir default to the os functions; tests root the
	// paths elsewhere.
	Lstat   func(string) (fs.FileInfo, error)
	Stat    func(string) (fs.FileInfo, error)
	ReadDir func(string) ([]fs.DirEntry, error)
}

// Scan checks the file system. The server runs it before config.Load and
// db.Open, which create the secrets file and the database a fresh install
// lacks. Users is left unknown for the caller to fill in.
func (p Probe) Scan() Facts {
	lstat, stat, readDir := p.Lstat, p.Stat, p.ReadDir
	if lstat == nil {
		lstat = os.Lstat
	}
	if stat == nil {
		stat = os.Stat
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
		if err != nil || active == nil || !sameFile(stat, fi, active, known, db) {
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
		filepath.Join(dataDir, restore.StagingDir),
		filepath.Join(dataDir, restore.PreRestoreDir),
	}
	for _, d := range data {
		if d != "" && present(lstat, d) {
			f.Data = true
		}
	}
	if stacks := p.Paths.StacksRoot; stacks != "" {
		if hasStack(readDir, stacks) {
			f.Data = true
		}
		if present(lstat, filepath.Clean(stacks)+restore.StagingDir) {
			f.Data = true
		}
		parent, base := filepath.Split(filepath.Clean(stacks))
		if entries, err := readDir(parent); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), base+restore.PreRestoreDir+"-") {
					f.Data = true
				}
			}
		}
	}
	return f
}

// hasStack reports a stack directory below root. Dot entries (.gitkeep)
// and lost+found of a separate volume are not stacks; a root that cannot
// be read may hold some.
func hasStack(readDir func(string) ([]fs.DirEntry, error), root string) bool {
	entries, err := readDir(root)
	if err != nil {
		return !isNotExist(err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "lost+found" {
			return true
		}
	}
	return false
}

// sameFile compares two database paths, following symlinks only for the
// comparison; Scan's Lstat keeps a dangling link counted as present.
func sameFile(stat func(string) (fs.FileInfo, error), a, b fs.FileInfo, pathA, pathB string) bool {
	if os.SameFile(a, b) {
		return true
	}
	ta, errA := stat(pathA)
	tb, errB := stat(pathB)
	return errA == nil && errB == nil && os.SameFile(ta, tb)
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

// CodeAllowed reports whether `dockmesh setup new-code` may create a code
// for an install with facts f (Users filled in). serverInSetup asks the
// running server; it is only asked when the files alone say set up,
// because a server in setup mode creates its database and secrets on its
// first start, and it can only add to what the files allow.
func CodeAllowed(f Facts, serverInSetup func() bool) bool {
	if f.Users != 0 || f.ForeignDB {
		return false
	}
	if Decide(f) != SetUp {
		return true
	}
	return serverInSetup()
}
