//go:build unix

package setup

import (
	"errors"
	"fmt"
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

// ensureCodeDir creates dir root:gid 0750, or lets gid traverse an
// existing one: the server reads the code file with its own group, which
// on older units is not the group of /etc/dockmesh. Others may only
// traverse, never list; the config next to it stays 0640.
func ensureCodeDir(dir string, gid int) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o750); err != nil {
			return err
		}
		return os.Chown(dir, 0, gid)
	}
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || st.Uid != 0 {
		return fmt.Errorf("%s is not a directory owned by root", dir)
	}
	if int(st.Gid) != gid && fi.Mode().Perm()&0o001 == 0 {
		return os.Chmod(dir, fi.Mode().Perm()|0o001)
	}
	return nil
}

// writeRootFile replaces path with data: a temp file in the same
// directory, fsync, rename.
func writeRootFile(path string, data []byte, gid int) error {
	dir := filepath.Dir(path)
	if err := ensureCodeDir(dir, gid); err != nil {
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

func errUntrusted(format string, a ...any) error {
	return fmt.Errorf("untrusted setup code file: "+format, a...)
}
