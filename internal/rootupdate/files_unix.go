//go:build unix

package rootupdate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// rootOwners are the owners rootOnlyDir accepts; tests add their own uid.
var rootOwners = []uint32{0}

// openRequestDir opens the service's update directory. It must be a
// directory owned by uid that nobody else may write, so a path swapped for
// a symlink to some other place is refused before root writes into it.
func openRequestDir(path string, uid int) (*os.Root, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	fi, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.IsDir() || int(st.Uid) != uid || fi.Mode().Perm()&0o002 != 0 {
		root.Close()
		return nil, fmt.Errorf("%s must be a directory owned by uid %d and not writable by others", path, uid)
	}
	return root, nil
}

// openUntrusted opens name below root for reading: a regular file with one
// link and at most max bytes. A symlink, FIFO, device, directory or a hard
// link to another file is refused. The open never blocks on a FIFO and
// never follows a symlink, and the file opened must be the one checked.
func openUntrusted(root *os.Root, name string, max int64) (*os.File, error) {
	li, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := checkRegular(name, li, max); err != nil {
		return nil, err
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink", name)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = checkRegular(name, fi, max)
	}
	if err == nil && !os.SameFile(li, fi) {
		err = fmt.Errorf("%s changed while it was opened", name)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func checkRegular(name string, fi fs.FileInfo, max int64) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (%s)", name, fi.Mode().Type())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return fmt.Errorf("%s has more than one link", name)
	}
	if fi.Size() > max {
		return fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return nil
}

// readUntrusted reads a file openUntrusted accepts.
func readUntrusted(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := openUntrusted(root, name, max)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return data, nil
}

// writeServiceFile replaces name below root with data, mode 0644. The
// file belongs to root; the service may read and remove it.
func writeServiceFile(root *os.Root, name string, data []byte) error {
	tmp := "." + name + "." + randSuffix()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

// removeEntry removes name below root whatever it is; a symlink is removed,
// not followed.
func removeEntry(root *os.Root, name string) error {
	err := root.RemoveAll(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// rootOnlyDir reports an error unless dir and every directory above it is
// owned by root and writable by nobody else (sticky directories aside), so
// no other account can swap a file root installs there.
func rootOnlyDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not an absolute path", dir)
	}
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.IsDir() || !slices.Contains(rootOwners, st.Uid) || fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s must be a directory owned by root and writable only by root", p)
		}
		if p == "/" {
			return nil
		}
	}
}

// ensureStateDir creates dir 0700 for root, or checks that it already is.
func ensureStateDir(dir string) error {
	if err := rootOnlyDir(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := rootOnlyDir(dir); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
