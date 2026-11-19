//go:build unix

package layout

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
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

// RestoreServerUnit puts the unit WriteServerUnit replaced back, headed
// by RestoredUnitMarker.
func RestoreServerUnit(opts UnitWriteOptions) error {
	unitPath, prevPath := opts.paths()
	old, err := readUnit(prevPath)
	if err != nil {
		return fmt.Errorf("no earlier unit to restore: %w", err)
	}
	if !restoredUnit(old) {
		old = append([]byte(RestoredUnitMarker+"\n"), old...)
	}
	return writeRootFileAtomic(unitPath, old, 0o644)
}

// WriteUpdateUnits writes the root updater's path and service units for
// binary and dataDir; changed reports whether either file changed. dir
// defaults to the directory of UpdatePathUnitFile.
func WriteUpdateUnits(binary, dataDir, dir string) (changed bool, err error) {
	pathUnit, serviceUnit, err := RenderUpdateUnits(binary, dataDir)
	if err != nil {
		return false, err
	}
	if dir == "" {
		dir = filepath.Dir(UpdatePathUnitFile)
	}
	paths, err := writeUnits(map[string][]byte{
		filepath.Join(dir, filepath.Base(UpdatePathUnitFile)):    pathUnit,
		filepath.Join(dir, filepath.Base(UpdateServiceUnitFile)): serviceUnit,
	})
	return len(paths) > 0, err
}

// WriteAgentUnits writes the agent's unit and its root updater's path and
// service units and returns the files it changed. Every unit is rendered
// before the first is written, so a refused value changes nothing. dir
// defaults to the directory of AgentUnitFile.
func WriteAgentUnits(u AgentUnit, dir string) (changed []string, err error) {
	unit, err := RenderAgentUnit(u)
	if err != nil {
		return nil, err
	}
	pathUnit, serviceUnit, err := RenderAgentUpdateUnits(u.Binary, u.DataDir)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		dir = filepath.Dir(AgentUnitFile)
	}
	return writeUnits(map[string][]byte{
		filepath.Join(dir, filepath.Base(AgentUnitFile)):              unit,
		filepath.Join(dir, filepath.Base(AgentUpdatePathUnitFile)):    pathUnit,
		filepath.Join(dir, filepath.Base(AgentUpdateServiceUnitFile)): serviceUnit,
	})
}

func writeUnits(files map[string][]byte) (changed []string, err error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		c, err := WriteRootFile(p, files[p], os.Geteuid(), os.Getegid(), 0o644)
		if err != nil {
			return changed, err
		}
		if c {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// WriteRootFile replaces path with data, owned by uid:gid with mode, unless
// it already is exactly that; changed reports a write. A symlink or
// anything but a regular file at path is refused.
func WriteRootFile(path string, data []byte, uid, gid int, mode os.FileMode) (changed bool, err error) {
	old, err := readUnit(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, err
	case bytes.Equal(old, data) && HasOwnerMode(path, uid, gid, mode):
		return false, nil
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer root.Close()
	if err := writeAtomic(root, filepath.Base(path), data, uid, gid, mode); err != nil {
		return false, err
	}
	return true, nil
}

// HasOwnerMode reports a regular file, not a symlink, owned by uid:gid
// with mode.
func HasOwnerMode(path string, uid, gid int, mode os.FileMode) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid && int(st.Gid) == gid && fi.Mode().Perm() == mode
}

// WriteRootFileFrom replaces path with what r yields, owned by uid:gid
// with mode, through a temp file in the same directory and a rename. For
// files too large for WriteRootFile, such as a binary.
func WriteRootFileFrom(path string, r io.Reader, uid, gid int, mode os.FileMode) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return writeAtomicFrom(root, filepath.Base(path), r, uid, gid, mode)
}

// RootOnlyDir reports whether root owns dir and every directory above it,
// none of them a symlink or writable by others.
func RootOnlyDir(dir string) bool { return rootOnlyPath(dir) }

// ReadUnitFile returns the unit file at path, nil when it does not exist.
// A symlink or anything but a regular file is refused.
func ReadUnitFile(path string) ([]byte, error) {
	data, err := readUnit(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// PutUnitFile replaces the unit file at path with data, or removes it
// when data is nil; for putting back what ReadUnitFile returned.
func PutUnitFile(path string, data []byte) error {
	if data == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeRootFileAtomic(path, data, 0o644)
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
	return PrepareRootDir(ConfigDir, 0, gid)
}

// PrepareRootDir creates dir or resets it to uid:gid 0750, like
// PrepareConfigDir for another config dir.
func PrepareRootDir(dir string, uid, gid int) error {
	return ensureConfigDir(dir, uid, gid)
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
	// O_NONBLOCK: a FIFO swapped in after the caller's Lstat must not
	// block root's open; on a regular file it changes nothing.
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
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

// privilegedGroups are never handed to a service through its socket:
// each of them is root, or reads secrets or raw disks, on its own.
var privilegedGroups = map[string]bool{
	"root": true, "wheel": true, "sudo": true, "admin": true, "adm": true, "shadow": true, "disk": true,
	"kmem": true, "lxd": true, "incus": true, "incus-admin": true, "libvirt": true, "kvm": true,
	"microk8s": true, "systemd-journal": true, "tty": true, "input": true, "video": true,
}

// PrivilegedGroup reports a group that is never handed to a service.
func PrivilegedGroup(name string) bool { return privilegedGroups[name] }

// SocketGroups returns the supplementary groups a service needs for Docker:
// "docker" when that group exists (dockerGID >= 0), and the numeric gid of
// the socket when another group owns it, as on Synology or with Podman;
// warn says why the socket's group was not added.
func SocketGroups(socket string, dockerGID int) (groups []string, warn string) {
	if dockerGID >= 0 {
		groups = append(groups, "docker")
	}
	if socket == "" {
		return groups, ""
	}
	resolved, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return groups, ""
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		return groups, ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Gid) == dockerGID {
		return groups, ""
	}
	gid := strconv.FormatUint(uint64(st.Gid), 10)
	name := gid
	if g, err := user.LookupGroupId(gid); err == nil {
		name = g.Name
	}
	switch {
	case fi.Mode()&os.ModeSocket == 0 || st.Uid != 0:
		return groups, fmt.Sprintf("%q is not a socket owned by root; its group is not added", socket)
	case !rootOnlyPath(socket) || !rootOnlyPath(resolved):
		return groups, fmt.Sprintf("%q lies on a path other accounts may change; its group is not added", socket)
	case st.Gid == 0 || privilegedGroups[name]:
		return groups, fmt.Sprintf("%q belongs to group %s, which is never given to the service", socket, name)
	}
	return append(groups, gid), ""
}

// rootOnlyPath reports whether root owns every component of p, without
// following symlinks, and no directory on it is writable by others.
func rootOnlyPath(p string) bool {
	for c := filepath.Clean(p); ; c = filepath.Dir(c) {
		fi, err := os.Lstat(c)
		if err != nil {
			return false
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.IsDir() && fi.Mode().Perm()&0o022 != 0 {
			return false
		}
		if c == "/" {
			return true
		}
	}
}

// RootOwnedFile reports whether root owns path and its directories and
// nobody else may write them, so values read from it may steer what root
// writes; a file in a directory another account owns could be swapped.
func RootOwnedFile(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0 && rootOnlyPath(filepath.Dir(path))
}
