package stackhelper

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func fileID(fi fs.FileInfo) (string, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("%s: no device and inode", fi.Name())
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}

// mountBelow returns the first mount point strictly below dir, relative to
// dir, or "" if there is none.
func mountBelow(dir string) (string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return mountBelowIn(f, dir)
}

func mountBelowIn(r io.Reader, dir string) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMount(fields[4])
		if rel, err := filepath.Rel(dir, mp); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			return rel, nil
		}
	}
	return "", sc.Err()
}

// unescapeMount undoes the octal escapes (\040 for a space) the kernel
// writes into mountinfo paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// canDeleteIn reports a permission error when entries of dir cannot be
// unlinked: that needs write and search access to dir.
func canDeleteIn(dir string) error {
	if err := unix.Access(dir, unix.W_OK|unix.X_OK); err != nil {
		return &fs.PathError{Op: "access", Path: dir, Err: err}
	}
	return nil
}
