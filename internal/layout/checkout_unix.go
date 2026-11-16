//go:build unix

package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// TrustedCheckout reports why dir must not serve as the data dir of a
// process running as euid, nil when it may. A directory someone else can
// write would let them plant the database, keys and secrets a root process
// then uses or writes through, so dir, dir/data and the database must be
// real files of root or euid that nobody else can write, the database must
// have one link, and every directory above dir must be root's or euid's
// and closed to others (a sticky /tmp is not).
func TrustedCheckout(dir string, euid int) error {
	return trustedCheckout(dir, euid, "/")
}

// trustedCheckout checks the directories above dir up to and including top.
func trustedCheckout(dir string, euid int, top string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%s is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if real != dir {
		return fmt.Errorf("%s leads through a symlink to %s", dir, real)
	}
	db := dbPathBelow(dir)
	for _, p := range []string{dir, filepath.Dir(db), db} {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if p == db && !fi.Mode().IsRegular() || p != db && !fi.IsDir() {
			return fmt.Errorf("%s has the wrong file type", p)
		}
		if err := closedToOthers(fi, euid); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if st := fi.Sys().(*syscall.Stat_t); p == db && st.Nlink != 1 {
			return fmt.Errorf("%s has more than one hard link", p)
		}
	}
	for p := filepath.Dir(dir); below(p, top); p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if err := closedToOthers(fi, euid); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if p == top {
			break
		}
	}
	return nil
}

func closedToOthers(fi os.FileInfo, euid int) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine owner")
	}
	if int(st.Uid) != 0 && int(st.Uid) != euid {
		return fmt.Errorf("owned by uid %d", st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return errors.New("writable by group or others")
	}
	return nil
}

func below(p, top string) bool {
	rel, err := filepath.Rel(top, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
