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
	// CheckoutDB: the working directory holds the database of a source
	// run that layout.TrustedCheckout refused as the data dir.
	CheckoutDB bool
	// DataDirSet is DOCKMESH_DATA_DIR set by the caller.
	DataDirSet bool
	// Users in the server's database; negative when unknown.
	Users int
}

// RefusesStart reports a start in a source run checkout whose data the
// server may not use, with nothing else configured: serving would create
// an empty install at the default paths next to the real one.
func RefusesStart(f Facts) bool {
	return f.CheckoutDB && !f.DB && !f.Config && !f.DataDirSet
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

// PurgedMarker is the file `uninstall.sh --purge` leaves in a data root
// that still holds what it keeps on purpose: stacks a container uses or
// with files Dockmesh did not write, and what a restore set aside. Those
// then don't count as data of an install; the database, its keys and the
// agent CA still do. Only an account that may write the data root can
// create it.
const PurgedMarker = ".dockmesh-purged"

// Probe lists where an install may keep its config and data.
type Probe struct {
	// ConfigFiles are extra env files to look for: --env-file and the one
	// the service unit names. layout.ConfigFile and layout.LegacyEnvFiles
	// are always checked.
	ConfigFiles []string
	// DBPathSet is DOCKMESH_DB_PATH being set.
	DBPathSet bool
	// DataDirSet is DOCKMESH_DATA_DIR set by the caller, not by the
	// checkout probe.
	DataDirSet bool
	// CheckoutDistrusted: layout.CheckoutProbe refused WorkDir.
	CheckoutDistrusted bool
	Force              bool
	// Paths of the server; layout.KnownDBPaths are checked as well.
	Paths config.Paths
	// WorkDir is the server's working directory when it holds the
	// database of a source run (layout.HasCheckoutDB); empty for none.
	WorkDir string
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
	f := Facts{Force: p.Force, DataDirSet: p.DataDirSet, Users: -1}
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
	for _, known := range layout.KnownDBPaths(p.WorkDir) {
		if filepath.Clean(known) == filepath.Clean(db) {
			continue
		}
		fi, err := lstat(known)
		if isNotExist(err) {
			continue
		}
		if err != nil || active == nil || !sameFile(stat, fi, active, known, db) {
			f.ForeignDB = true
			if p.CheckoutDistrusted && p.WorkDir != "" && known == layout.CheckoutDBPath(p.WorkDir) {
				f.CheckoutDB = true
			}
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
	if p.Paths.SecretsKeyPath != "" {
		data = append(data, p.Paths.SecretsKeyPath+".history")
	}
	purged := p.Paths.DataDir != "" && regularFile(lstat, filepath.Join(p.Paths.DataDir, PurgedMarker))
	if !purged {
		data = append(data, filepath.Join(dataDir, restore.StagingDir), filepath.Join(dataDir, restore.PreRestoreDir))
	}
	for _, d := range data {
		if d != "" && present(lstat, d) {
			f.Data = true
		}
	}
	if stacks := p.Paths.StacksRoot; stacks != "" && !purged {
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

// regularFile reports a regular file at path, not a symlink to one.
func regularFile(lstat func(string) (fs.FileInfo, error), path string) bool {
	fi, err := lstat(path)
	return err == nil && fi.Mode().IsRegular()
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
