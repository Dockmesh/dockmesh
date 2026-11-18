//go:build unix

package stackfiles

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
)

// Apply writes set into the stack directory opened as root and removes the
// files of prev that are no longer in set. A file is replaced or removed
// only while its content is still what prev recorded; a file that matches
// the set already is adopted. Everything else stays and is listed in the
// report. The returned manifest replaces prev.
func Apply(root *os.Root, set *Set, prev *Manifest) (*Manifest, Report, error) {
	var rep Report
	if err := set.Validate(); err != nil {
		return nil, rep, err
	}
	if prev == nil {
		prev = &Manifest{}
	}
	next := &Manifest{Files: map[string]string{}, Sizes: map[string]int64{}}
	created := map[string]bool{}
	for _, d := range prev.Dirs {
		created[d] = true
	}
	a := &applier{root: root, created: created, rep: &rep}

	files := append([]File(nil), set.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	inSet := make(map[string]bool, len(files))
	for _, f := range files {
		inSet[f.Path] = true
		if f.Dir {
			a.dir(f)
			continue
		}
		if sum, ok := a.file(f, prev.record(f.Path)); ok {
			next.Files[f.Path] = sum
			next.Sizes[f.Path] = int64(len(f.Data))
		}
	}

	for _, p := range sortedKeys(prev.Files) {
		if inSet[p] {
			continue
		}
		a.remove(p, prev.record(p))
	}

	// Directories Dockmesh created and the set no longer has go once
	// empty; deepest first, so a parent can follow its children.
	dirs := make([]string, 0, len(created))
	for d := range created {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i] > dirs[j] })
	for _, d := range dirs {
		if !inSet[d] && !hasPrefixDir(next.Files, d) {
			// Remove would unlink a file that replaced the directory.
			if fi, err := root.Lstat(d); err == nil && fi.IsDir() {
				_ = root.Remove(d)
			}
		}
		if fi, err := root.Lstat(d); err == nil && fi.IsDir() {
			next.Dirs = append(next.Dirs, d)
		}
	}
	sort.Strings(next.Dirs)
	return next, rep, nil
}

// record is what Dockmesh wrote at p last time; size is -1 when unknown.
type record struct {
	sum  string
	size int64
}

func (m *Manifest) record(p string) record {
	r := record{sum: m.Files[p], size: -1}
	if n, ok := m.Sizes[p]; ok {
		r.size = n
	}
	return r
}

// sameSize reports whether a file of size n may still be the one recorded.
func (r record) sameSize(n int64) bool {
	if r.sum == "" {
		return false
	}
	if r.size < 0 {
		return n <= MaxTotalBytes
	}
	return n == r.size
}

type applier struct {
	root    *os.Root
	created map[string]bool
	rep     *Report
}

func (a *applier) keep(p, reason string) {
	a.rep.Kept = append(a.rep.Kept, Note{Path: p, Reason: reason})
}

// mkdirAll creates the missing directories of p, recording each.
func (a *applier) mkdirAll(p string, mode fs.FileMode) error {
	if p == "." || p == "" {
		return nil
	}
	fi, err := a.root.Lstat(p)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s: %w", p, syscall.ENOTDIR)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := a.mkdirAll(path.Dir(p), 0o755); err != nil {
		return err
	}
	if err := a.root.Mkdir(p, mode); err != nil {
		return err
	}
	a.created[p] = true
	fi, err = a.root.Lstat(p)
	if err != nil {
		return err
	}
	// Mkdir is subject to the umask; the server's mode is what counts.
	return a.chmodDir(p, fi, mode)
}

// chmodDir changes the mode of the directory Lstat saw at p, never of
// what a symlink swapped in meanwhile points to.
func (a *applier) chmodDir(p string, fi fs.FileInfo, mode fs.FileMode) error {
	if !fi.IsDir() {
		return errChanged
	}
	f, err := a.root.OpenFile(p, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, got) {
		return errChanged
	}
	return f.Chmod(mode)
}

func (a *applier) dir(f File) {
	mode := fs.FileMode(f.Mode) & fs.ModePerm
	fi, err := a.root.Lstat(f.Path)
	switch {
	case err == nil && fi.IsDir():
		if a.created[f.Path] && fi.Mode().Perm() != mode {
			_ = a.chmodDir(f.Path, fi, mode)
		}
	case err == nil:
		a.keep(f.Path, "a file is in the way of this directory")
	default:
		if err := a.mkdirAll(f.Path, mode); err != nil {
			a.keep(f.Path, writeReason(err))
		}
	}
}

// file writes one regular file and returns the sum to record, or false
// when the file is not Dockmesh's (any more).
func (a *applier) file(f File, prev record) (string, bool) {
	mode := fs.FileMode(f.Mode) & fs.ModePerm
	want := sum(f.Data)
	fi, err := a.root.Lstat(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := a.mkdirAll(path.Dir(f.Path), 0o755); err != nil {
			a.keep(f.Path, writeReason(err))
			return "", false
		}
		if err := a.create(f.Path, f.Data, mode); err != nil {
			a.keep(f.Path, writeReason(err))
			return "", false
		}
		a.rep.Copied++
		return want, true
	}
	if err != nil {
		a.keep(f.Path, writeReason(err))
		return "", false
	}
	switch {
	case fi.IsDir():
		a.keep(f.Path, "a directory is in the way (Docker creates one for a bind source that is missing); remove it on the host and deploy again")
		return "", false
	case !fi.Mode().IsRegular():
		a.keep(f.Path, "a symlink or special file is in the way")
		return "", false
	}
	// Only a file of the right size is read, so files planted at the set's
	// paths can't make every deploy read gigabytes.
	cur := ""
	if fi.Size() == int64(len(f.Data)) || prev.sameSize(fi.Size()) {
		if cur, err = a.hash(f.Path, fi); err != nil {
			a.keep(f.Path, writeReason(err))
			return "", false
		}
	}
	switch {
	case cur == want:
		if fi.Mode().Perm() != mode {
			if err := a.chmod(f.Path, fi, mode); err != nil {
				a.keep(f.Path, writeReason(err))
				return "", false
			}
		}
	case cur != "" && cur == prev.sum:
		if err := a.overwrite(f.Path, fi, f.Data, mode); err != nil {
			a.keep(f.Path, writeReason(err))
			return "", false
		}
	case prev.sum != "":
		a.keep(f.Path, "changed on the host since the last deploy")
		return "", false
	default:
		a.keep(f.Path, "not written by Dockmesh")
		return "", false
	}
	a.rep.Copied++
	return want, true
}

// remove deletes p while it holds what Dockmesh wrote. A container that
// replaces the file between the check and the unlink loses that file;
// the window is a few syscalls and confined to the stack directory.
func (a *applier) remove(p string, prev record) {
	fi, err := a.root.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	cur := ""
	if prev.sameSize(fi.Size()) {
		if cur, err = a.hash(p, fi); err != nil {
			return
		}
	}
	if cur == "" || cur != prev.sum {
		a.keep(p, "removed on the server but changed on the host since the last deploy")
		return
	}
	if err := a.root.Remove(p); err != nil {
		a.keep(p, "removed on the server, but: "+writeReason(err))
		return
	}
	a.rep.Removed++
}

// open opens p and makes sure it is still the file Lstat saw: os.Root
// follows a symlink swapped in meanwhile (inside the root only), and the
// write must not land in the file it points to.
func (a *applier) open(p string, fi fs.FileInfo, flag int) (*os.File, error) {
	// O_NONBLOCK: opening a FIFO swapped in meanwhile must not block.
	f, err := a.root.OpenFile(p, flag|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	got, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !got.Mode().IsRegular() || !os.SameFile(fi, got) {
		f.Close()
		return nil, errChanged
	}
	return f, nil
}

var errChanged = errors.New("replaced while it was written")

func (a *applier) hash(p string, fi fs.FileInfo) (string, error) {
	f, err := a.open(p, fi, os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxTotalBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *applier) chmod(p string, fi fs.FileInfo, mode fs.FileMode) error {
	f, err := a.open(p, fi, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(mode)
}

// overwrite rewrites an existing file in place rather than replacing it,
// so a container that bind-mounts the single file sees the new content.
func (a *applier) overwrite(p string, fi fs.FileInfo, data []byte, mode fs.FileMode) error {
	f, err := a.open(p, fi, os.O_WRONLY)
	if errors.Is(err, fs.ErrPermission) {
		// A read-only mode the server sent last time.
		if err := a.chmod(p, fi, 0o600); err != nil {
			return err
		}
		f, err = a.open(p, fi, os.O_WRONLY)
	}
	if err != nil {
		return err
	}
	return writeAll(f, data, mode, true)
}

// create writes a new file under a temporary name and renames it into
// place, so a half-written file never appears under the real name.
func (a *applier) create(p string, data []byte, mode fs.FileMode) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(p), tempPrefix+hex.EncodeToString(rnd[:]))
	f, err := a.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := writeAll(f, data, mode, false); err != nil {
		_ = a.root.Remove(tmp)
		return err
	}
	defer a.root.Remove(tmp)
	// A hard link never replaces what a container put at p meanwhile.
	err = a.root.Link(tmp, p)
	if errors.Is(err, fs.ErrExist) {
		return errChanged
	}
	if err == nil {
		return nil
	}
	// File systems without hard links: a rename after a last check.
	if _, err := a.root.Lstat(p); err == nil {
		return errChanged
	}
	return a.root.Rename(tmp, p)
}

func writeAll(f *os.File, data []byte, mode fs.FileMode, truncate bool) error {
	if truncate {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeReason turns a write error into the short text the deploy result
// shows; paths inside err are the agent's and stay out of it.
func writeReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "the agent may not write here; check the owner of the file or its directory on the host"
	case errors.Is(err, errChanged):
		return "changed on the host while it was written"
	case errors.Is(err, syscall.ENOSPC):
		return "no space left on the host"
	case errors.Is(err, syscall.ENOTDIR):
		return "a file is in the way of its directory"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasPrefixDir(files map[string]string, dir string) bool {
	for p := range files {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}
