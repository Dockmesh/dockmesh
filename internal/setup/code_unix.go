//go:build unix

package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// trustCodeFile accepts only a file root (or the server's own account,
// which owns the install anyway) owns and nobody else may write: anything
// else could have been planted by another account.
func trustCodeFile(fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errUntrusted("cannot determine owner")
	}
	if st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return errUntrusted("owned by uid %d, not root", st.Uid)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
		return errUntrusted("not a regular file only root may write")
	}
	return nil
}

// writeRootFile replaces path with data: a temp file in the same
// directory, fsync, rename. The directory is created 0750 if missing.
func writeRootFile(path string, data []byte, gid int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".setup-code-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chown(0, gid); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
