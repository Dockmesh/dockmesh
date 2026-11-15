//go:build unix

package setup

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// RootOwnedAncestors refuses dir unless it and every directory above it
// belong to root and only root may write them, so no other account can
// swap one of them for a symlink.
func RootOwnedAncestors(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not absolute", dir)
	}
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		owner, ok := FileOwner(fi)
		if !fi.IsDir() || !ok || owner != 0 || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s must be a directory owned by root that only root may write", p)
		}
		if p == "/" {
			return nil
		}
	}
}

// FileOwner is the uid that owns fi.
func FileOwner(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// ownedBySelf reports whether the account setup-apply hands the data dir
// to owns fi: the dockmesh account when it exists, else this process's.
func ownedBySelf(fi fs.FileInfo) bool {
	owner, ok := FileOwner(fi)
	if !ok {
		return false
	}
	want := strconv.Itoa(os.Getuid())
	if u, err := user.Lookup("dockmesh"); err == nil {
		want = u.Uid
	}
	return strconv.FormatUint(uint64(owner), 10) == want
}
