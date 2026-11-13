//go:build unix

package layout

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/config"
)

// Migrate moves the first candidate env file to <ConfigDir>/dockmesh.env,
// adds DOCKMESH_DATA_DIR naming the directory the data is in today, and
// leaves a symlink at the old place so a unit or script that names the old
// path keeps working. It never moves data. On any error the old file is
// left as it was.
func Migrate(opts MigrateOptions) (Result, error) {
	dir := opts.ConfigDir
	if dir == "" {
		dir = ConfigDir
	}
	target := filepath.Join(dir, envFileName)
	switch fi, err := os.Lstat(target); {
	case err == nil && fi.Mode().IsRegular():
		return Result{Outcome: AlreadyMigrated, From: target}, nil
	case err == nil:
		return Result{}, fmt.Errorf("%s exists but is not a regular file", target)
	case !errors.Is(err, fs.ErrNotExist):
		return Result{}, err
	}

	src := findSource(opts.Candidates)
	if src == "" {
		return Result{Outcome: NothingFound}, nil
	}
	srcRoot, err := os.OpenRoot(filepath.Dir(src))
	if err != nil {
		return Result{}, err
	}
	defer srcRoot.Close()
	data, srcInfo, err := readSource(srcRoot, src, opts.UID, opts.GID)
	if err != nil {
		return Result{}, err
	}
	env := parseEnv(data)
	if _, ok := env["DOCKMESH_SETUP_FORCE"]; ok {
		return Result{Outcome: SetupPending, From: src}, nil
	}
	dataDir := env["DOCKMESH_DATA_DIR"]
	if dataDir == "" {
		dataDir = config.InferDataDir(env["DOCKMESH_DB_PATH"])
		if dataDir == "" {
			dataDir = filepath.Dir(src)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, "# Added when this file moved from "+src+"; the data stays there.\nDOCKMESH_DATA_DIR="+dataDir+"\n"...)
	}

	if err := ensureConfigDir(dir, opts.UID, opts.GID); err != nil {
		return Result{}, err
	}
	dstRoot, err := os.OpenRoot(dir)
	if err != nil {
		return Result{}, err
	}
	defer dstRoot.Close()
	if err := writeAtomic(dstRoot, envFileName, data, opts.UID, opts.GID, targetMode(srcInfo, opts.UID)); err != nil {
		return Result{}, err
	}
	if err := linkOldPlace(srcRoot, srcInfo, target); err != nil {
		if rmErr := dstRoot.Remove(envFileName); rmErr != nil {
			return Result{}, fmt.Errorf("%w; removing %s again failed too: %v", err, target, rmErr)
		}
		return Result{}, err
	}
	return Result{Outcome: Moved, From: src, DataDir: dataDir}, nil
}

// findSource returns the first candidate that is a regular file named
// dockmesh.env. Symlinks are skipped: after a move the old place is one.
func findSource(candidates []string) string {
	for _, c := range candidates {
		if c == "" || filepath.Base(c) != envFileName || !filepath.IsAbs(c) {
			continue
		}
		if fi, err := os.Lstat(c); err == nil && fi.Mode().IsRegular() {
			return filepath.Clean(c)
		}
	}
	return ""
}

// readSource reads the env file the way root may trust it: not a link,
// one hard link only (a link to a root-owned file elsewhere would copy
// that file into /etc), and, like its directory, owned by root, the
// service user or a deleted account and writable by nobody else.
func readSource(root *os.Root, src string, selfUID, gid int) ([]byte, os.FileInfo, error) {
	dirInfo, err := root.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	if err := checkTrusted(dirInfo, selfUID); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrUntrusted, filepath.Dir(src), err)
	}
	f, err := root.OpenFile(envFileName, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s is not a regular file", ErrUntrusted, src)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return nil, nil, fmt.Errorf("%w: %s has more than one hard link", ErrUntrusted, src)
	}
	if err := checkTrusted(fi, selfUID); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrUntrusted, src, err)
	}
	// A root file readable by some other group (/etc/shadow) is no config.
	if st := fi.Sys().(*syscall.Stat_t); (int(st.Uid) == 0 || int(st.Uid) == selfUID) &&
		fi.Mode().Perm()&0o040 != 0 && st.Gid != 0 && int(st.Gid) != gid {
		return nil, nil, fmt.Errorf("%w: %s is readable by group %d", ErrUntrusted, src, st.Gid)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEnvFileSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxEnvFileSize {
		return nil, nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUntrusted, src, maxEnvFileSize)
	}
	return data, fi, nil
}

// targetMode is 0640 for a config the service could already read, and
// 0600 for a root-owned file without group read: such a file may be a
// root-only secret renamed into place, and the copy must not hand it to
// the dockmesh group.
func targetMode(src os.FileInfo, selfUID int) os.FileMode {
	st, ok := src.Sys().(*syscall.Stat_t)
	if !ok {
		return 0o600
	}
	if (int(st.Uid) == 0 || int(st.Uid) == selfUID) && src.Mode().Perm()&0o040 == 0 {
		return 0o600
	}
	return 0o640
}

func checkTrusted(fi os.FileInfo, selfUID int) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine owner")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return errors.New("writable by group or others")
	}
	uid := int(st.Uid)
	if uid == 0 || uid == selfUID {
		return nil
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		// uninstall.sh deletes the service user but keeps its data.
		return nil
	}
	if u.Username == "dockmesh" {
		return nil
	}
	return fmt.Errorf("owned by %s", u.Username)
}

func ensureConfigDir(dir string, uid, gid int) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o750); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	if err := os.Lchown(dir, uid, gid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o750)
}

// writeAtomic writes name below root through a temp file and a rename,
// so a reader sees the old state or the complete new file.
func writeAtomic(root *os.Root, name string, data []byte, uid, gid int, mode os.FileMode) error {
	tmp := "." + name + ".new-" + randSuffix()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	ok = true
	return syncDir(root)
}

// linkOldPlace replaces the moved env file with a symlink to target. It
// refuses when the file changed since it was read, so no edit is lost.
func linkOldPlace(root *os.Root, read os.FileInfo, target string) error {
	now, err := root.Lstat(envFileName)
	if err != nil {
		return err
	}
	if !os.SameFile(now, read) || now.ModTime() != read.ModTime() || now.Size() != read.Size() {
		return errors.New("the env file changed while it was being moved; run the command again")
	}
	tmp := "." + envFileName + ".link-" + randSuffix()
	if err := root.Symlink(target, tmp); err != nil {
		return err
	}
	if err := root.Rename(tmp, envFileName); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return syncDir(root)
}

func syncDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
