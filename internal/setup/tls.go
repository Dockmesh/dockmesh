package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// StateDirName is the directory below the data dir that holds what setup
// mode keeps until the install is committed: the PKI, which signs the
// certificate setup mode serves.
const StateDirName = ".setup"

// StateDir is the setup state directory of dataDir.
func StateDir(dataDir string) string {
	return filepath.Join(dataDir, StateDirName)
}

// ServesTLS reports whether setup mode serves HTTPS. A new install does,
// from the first request; so does a restart of one, which left its state
// directory behind. An unfinished install from 0.5.x, which already has a
// database and no state directory, keeps the plain HTTP it had.
func ServesTLS(f Facts, dataDir string) bool {
	if !f.DB {
		return true
	}
	fi, err := os.Lstat(StateDir(dataDir))
	return err == nil && fi.IsDir()
}

// EnsureStateDir creates the setup state directory 0700, or checks an
// existing one is a directory and not a link, and returns the directory
// the PKI lives in.
func EnsureStateDir(dataDir string) (string, error) {
	dir := StateDir(dataDir)
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	case !fi.IsDir():
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return PKIDir(dataDir), nil
}

// PKIDir is where setup mode keeps its PKI, below StateDir.
func PKIDir(dataDir string) string {
	return filepath.Join(StateDir(dataDir), "data")
}
