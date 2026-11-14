//go:build unix

package layout

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func (o UnitWriteOptions) paths() (string, string) {
	unit, prev := o.UnitFile, o.PrevUnitFile
	if unit == "" {
		unit = UnitFile
	}
	if prev == "" {
		prev = PrevUnitFile
	}
	return unit, prev
}

// maxUnitSize bounds what is read back of an existing unit.
const maxUnitSize = 1 << 20

// WriteServerUnit renders u and replaces the unit with it. The unit it
// replaces is kept as PrevUnitFile first. Nothing is written when a value
// is refused or the unit is unchanged; changed reports a write. Drop-ins
// next to the unit are never touched.
func WriteServerUnit(u ServerUnit, opts UnitWriteOptions) (changed bool, err error) {
	data, err := RenderServerUnit(u)
	if err != nil {
		return false, err
	}
	if err := checkResolved(u); err != nil {
		return false, err
	}
	unitPath, prevPath := opts.paths()
	old, err := readUnit(unitPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		old = nil
		// A copy left by an earlier install must not be restored over
		// this one.
		if err := os.Remove(prevPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	case err != nil:
		return false, err
	case bytes.Equal(old, data):
		return false, nil
	}
	if old != nil {
		if err := writeRootFileAtomic(prevPath, old, 0o640); err != nil {
			return false, fmt.Errorf("keep the old unit: %w", err)
		}
	}
	if err := writeRootFileAtomic(unitPath, data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// RestoreServerUnit puts the unit WriteServerUnit replaced back.
func RestoreServerUnit(opts UnitWriteOptions) error {
	unitPath, prevPath := opts.paths()
	old, err := readUnit(prevPath)
	if err != nil {
		return fmt.Errorf("no earlier unit to restore: %w", err)
	}
	return writeRootFileAtomic(unitPath, old, 0o644)
}

// readUnit reads a regular file without following a symlink.
func readUnit(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink; not touching it", path)
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; not touching it", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxUnitSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUnitSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxUnitSize)
	}
	return data, nil
}

// writeRootFileAtomic replaces path with a root-owned file of the given
// mode through a temp file in the same directory and a rename.
func writeRootFileAtomic(path string, data []byte, mode os.FileMode) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return writeAtomic(root, filepath.Base(path), data, os.Geteuid(), os.Getegid(), mode)
}

// PrepareConfigDir creates ConfigDir or resets it to root:gid 0750, the
// mode the server's group may read and nobody else may enter.
func PrepareConfigDir(gid int) error {
	return ensureConfigDir(ConfigDir, 0, gid)
}

// ReadEnvFile returns the KEY=VALUE pairs of the env file a unit is
// written for. Like Migrate it refuses a symlink, a file another account
// may have planted and anything larger than a config.
func ReadEnvFile(path string) (map[string]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s is not an absolute path", path)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUntrusted, path)
	}
	if err := checkTrusted(fi, os.Geteuid()); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUntrusted, path, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEnvFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEnvFileSize {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUntrusted, path, maxEnvFileSize)
	}
	return parseEnv(data), nil
}

// CheckServerUnit refuses u for the same reasons WriteServerUnit does,
// without writing anything.
func CheckServerUnit(u ServerUnit) error {
	if _, err := RenderServerUnit(u); err != nil {
		return err
	}
	return checkResolved(u)
}

// checkResolved applies the location rules of readWritePaths to the
// paths behind symlinks too, since systemd follows them when it mounts
// ReadWritePaths=. A path that does not exist yet is skipped.
func checkResolved(u ServerUnit) error {
	resolve := func(p string) (string, bool) {
		r, err := filepath.EvalSymlinks(p)
		return r, err == nil
	}
	dataDir, ok := resolve(u.DataDir)
	if !ok {
		dataDir = filepath.Clean(u.DataDir)
	}
	if err := checkLocation("data dir", u.DataDir, dataDir, ""); err != nil {
		return err
	}
	for _, p := range u.WritablePaths {
		if r, ok := resolve(p); ok {
			if err := checkLocation("writable path", p, r, dataDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// SocketGroups returns the supplementary groups a service needs for Docker:
// "docker" when that group exists (dockerGID >= 0), and the numeric gid of
// the socket when another group owns it, as on Synology or with Podman. A
// socket owned by group root is not added: that would hand the service
// group root; warn says so. A missing socket adds nothing.
func SocketGroups(socket string, dockerGID int) (groups []string, warn string) {
	if dockerGID >= 0 {
		groups = append(groups, "docker")
	}
	if socket == "" {
		return groups, ""
	}
	fi, err := os.Stat(socket)
	if err != nil {
		return groups, ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Gid) == dockerGID {
		return groups, ""
	}
	if st.Gid == 0 {
		return groups, fmt.Sprintf("%s belongs to group root; the service gets no access to it through a group", socket)
	}
	return append(groups, strconv.FormatUint(uint64(st.Gid), 10)), ""
}
