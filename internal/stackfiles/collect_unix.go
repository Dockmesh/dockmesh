//go:build unix

package stackfiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"

	"golang.org/x/sys/unix"
)

// maxNotes bounds the skipped list a deploy reports.
const maxNotes = 100

// Collect reads the stack directory dir into a set. Entries the server
// cannot read (data another user's container wrote), symlinks, special
// files and names an agent would refuse are left out and listed. A
// directory over the limits returns a *TooLargeError.
//
// Containers may write the server's stack directory while it is read, so
// the walk goes from directory descriptor to directory descriptor and
// never follows a symlink: a directory swapped for a link to the stack
// root can't make Skip see .env under another name.
func Collect(dir string) (*Set, []Note, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	c := &collector{set: &Set{}, sizeOf: map[string]int64{}}
	if err := c.walk(fd, ""); err != nil {
		return nil, nil, err
	}
	if c.over {
		return nil, nil, c.tooLarge()
	}
	if c.dropped > 0 {
		c.skipped = append(c.skipped, Note{Path: "…", Reason: fmt.Sprintf("%d more entries left out", c.dropped)})
	}
	return c.set, c.skipped, nil
}

type collector struct {
	set            *Set
	skipped        []Note
	dropped        int
	sizeOf         map[string]int64
	total, encoded int64
	entries        int
	over           bool
}

func (c *collector) skip(p, reason string) {
	if len(c.skipped) < maxNotes {
		c.skipped = append(c.skipped, Note{Path: p, Reason: reason})
		return
	}
	c.dropped++
}

// walk reads the directory open as fd (closed on return), rel being its
// path in the stack directory.
func (c *collector) walk(fd int, rel string) error {
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	for {
		// In batches, so a directory with millions of names hits
		// MaxEntries before it is all in memory.
		names, err := f.Readdirnames(512)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		sort.Strings(names)
		if err := c.entriesOf(fd, rel, names); err != nil {
			return err
		}
	}
}

// entriesOf handles one batch of names listed in the directory fd.
func (c *collector) entriesOf(fd int, rel string, names []string) error {
	for _, name := range names {
		p := name
		if rel != "" {
			p = path.Join(rel, name)
		}
		// Names Skip leaves out count too: they cost the walk all the same.
		c.entries++
		if c.entries > MaxEntries {
			return &TooLargeError{Entries: c.entries}
		}
		if Skip(p) {
			continue
		}
		if err := checkName(p); err != nil {
			c.skip(p, err.Error())
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			c.skip(p, "not readable by the server")
			continue
		}
		mode := uint32(st.Mode & 0o777) //nolint:unconvert // Stat_t.Mode is uint16 on darwin and freebsd
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			// A link could name a file Skip keeps out (data/x -> ../.env),
			// and what it points to inside the directory is sent anyway.
			c.skip(p, "symlink, not copied")
		case unix.S_IFDIR:
			sub, err := openSame(fd, name, unix.O_DIRECTORY, &st)
			if err != nil {
				// Leave it out entirely, so the agent doesn't create it
				// with the server's mode.
				c.skip(p, "not readable by the server")
				continue
			}
			c.set.Files = append(c.set.Files, File{Path: p, Mode: mode, Dir: true})
			c.encoded += encodedSize(p, 0)
			if err := c.walk(sub, p); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := c.file(fd, name, p, mode, &st); err != nil {
				return err
			}
		default:
			c.skip(p, "not a regular file")
		}
	}
	return nil
}

func (c *collector) file(dirfd int, name, p string, mode uint32, st *unix.Stat_t) error {
	size := st.Size
	c.total += size
	c.encoded += encodedSize(p, size)
	c.sizeOf[p] = size
	if c.total > MaxTotalBytes || c.encoded > MaxEncodedBytes {
		c.over = true
	}
	if c.over {
		return nil
	}
	data, err := readSame(dirfd, name, st, MaxTotalBytes-(c.total-size))
	switch {
	case errors.Is(err, errGrew):
		c.over = true
		return nil
	case err != nil:
		c.total -= size
		c.encoded -= encodedSize(p, size)
		delete(c.sizeOf, p)
		if errors.Is(err, errNotRegular) {
			c.skip(p, "changed while it was read")
		} else {
			c.skip(p, "not readable by the server")
		}
		return nil
	}
	c.set.Files = append(c.set.Files, File{Path: p, Mode: mode, Data: data})
	return nil
}

func (c *collector) tooLarge() *TooLargeError {
	sizes := make([]Note, 0, len(c.sizeOf))
	for p, n := range c.sizeOf {
		sizes = append(sizes, Note{Path: p, Reason: mib(n)})
	}
	sort.Slice(sizes, func(i, j int) bool {
		if c.sizeOf[sizes[i].Path] != c.sizeOf[sizes[j].Path] {
			return c.sizeOf[sizes[i].Path] > c.sizeOf[sizes[j].Path]
		}
		return sizes[i].Path < sizes[j].Path
	})
	if len(sizes) > 3 {
		sizes = sizes[:3]
	}
	return &TooLargeError{Entries: c.entries, Bytes: c.total, Largest: sizes}
}

var (
	errGrew       = errors.New("file grew while it was read")
	errNotRegular = errors.New("not the entry that was listed")
)

// openSame opens name below dirfd without following a symlink and checks
// it is still the entry st describes. O_NONBLOCK keeps a FIFO swapped in
// meanwhile from blocking the open.
func openSame(dirfd int, name string, flags int, st *unix.Stat_t) (int, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC|flags, 0)
	if err != nil {
		return -1, err
	}
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if got.Dev != st.Dev || got.Ino != st.Ino || got.Mode&unix.S_IFMT != st.Mode&unix.S_IFMT {
		unix.Close(fd)
		return -1, errNotRegular
	}
	return fd, nil
}

// readSame reads the regular file st describes unless it grew past limit.
func readSame(dirfd int, name string, st *unix.Stat_t, limit int64) ([]byte, error) {
	fd, err := openSame(dirfd, name, 0, st)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errGrew
	}
	return data, nil
}
