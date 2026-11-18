package stackfiles

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"
)

// TooLargeError refuses a stack directory over the limits of one set.
type TooLargeError struct {
	Entries int
	Bytes   int64
	Largest []Note
}

func (e *TooLargeError) Error() string {
	var b strings.Builder
	if e.Entries > MaxEntries {
		fmt.Fprintf(&b, "the stack directory holds more than %d files and directories", MaxEntries)
	} else {
		fmt.Fprintf(&b, "the files next to compose.yaml add up to %s", mib(e.Bytes))
	}
	fmt.Fprintf(&b, ", more than a deploy carries to an agent (%d entries, %s of data, %s with the names)", MaxEntries, mib(MaxTotalBytes), mib(MaxEncodedBytes))
	if len(e.Largest) > 0 {
		parts := make([]string, len(e.Largest))
		for i, n := range e.Largest {
			parts[i] = n.Path + " (" + n.Reason + ")"
		}
		b.WriteString("; largest: " + strings.Join(parts, ", "))
	}
	b.WriteString(". Keep data in a volume and large files in the image")
	return b.String()
}

// Collect reads the stack directory dir into a set. Entries the server
// cannot read (data another user's container wrote), symlinks, special
// files and names an agent would refuse are left out and listed. A directory over the limits returns a *TooLargeError.
func Collect(dir string) (*Set, []Note, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	fsys := root.FS()

	set := &Set{}
	var skipped []Note
	dirIndex := map[string]int{}
	sizeOf := map[string]int64{}
	var total, encoded int64
	entries := 0
	over := false

	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			// A directory the server cannot list: leave it out entirely,
			// so the agent doesn't create it with the server's mode.
			if i, ok := dirIndex[p]; ok {
				set.Files[i].Path = ""
			}
			skipped = append(skipped, Note{Path: p, Reason: "not readable by the server"})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == "." {
			return nil
		}
		if Skip(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := checkName(p); err != nil {
			skipped = append(skipped, Note{Path: p, Reason: err.Error()})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// A symlink could name a file Skip keeps out (data/x -> ../.env),
		// and the files it points to inside the directory are sent anyway.
		if d.Type()&fs.ModeSymlink != 0 {
			skipped = append(skipped, Note{Path: p, Reason: "symlink, not copied"})
			return nil
		}
		entries++
		if entries > MaxEntries {
			return &TooLargeError{Entries: entries}
		}
		info, err := d.Info()
		if err != nil {
			skipped = append(skipped, Note{Path: p, Reason: "not readable by the server"})
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		mode := uint32(info.Mode().Perm())
		switch {
		case d.IsDir():
			dirIndex[p] = len(set.Files)
			set.Files = append(set.Files, File{Path: p, Mode: mode, Dir: true})
			encoded += encodedSize(p, 0)
			return nil
		case !info.Mode().IsRegular():
			skipped = append(skipped, Note{Path: p, Reason: "not a regular file"})
			return nil
		}
		total += info.Size()
		encoded += encodedSize(p, info.Size())
		sizeOf[p] = info.Size()
		if total > MaxTotalBytes || encoded > MaxEncodedBytes {
			over = true
		}
		if over {
			return nil
		}
		data, err := readCapped(root, p, MaxTotalBytes-(total-info.Size()))
		if errors.Is(err, fs.ErrPermission) {
			total -= info.Size()
			encoded -= encodedSize(p, info.Size())
			delete(sizeOf, p)
			skipped = append(skipped, Note{Path: p, Reason: "not readable by the server"})
			return nil
		}
		if errors.Is(err, errNotRegular) {
			total -= info.Size()
			encoded -= encodedSize(p, info.Size())
			delete(sizeOf, p)
			skipped = append(skipped, Note{Path: p, Reason: "not a regular file"})
			return nil
		}
		if errors.Is(err, errGrew) {
			over = true
			return nil
		}
		if err != nil {
			return err
		}
		set.Files = append(set.Files, File{Path: p, Mode: mode, Data: data})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if over {
		sizes := make([]Note, 0, len(sizeOf))
		for p, n := range sizeOf {
			sizes = append(sizes, Note{Path: p, Reason: mib(n)})
		}
		sort.Slice(sizes, func(i, j int) bool {
			if sizeOf[sizes[i].Path] != sizeOf[sizes[j].Path] {
				return sizeOf[sizes[i].Path] > sizeOf[sizes[j].Path]
			}
			return sizes[i].Path < sizes[j].Path
		})
		if len(sizes) > 3 {
			sizes = sizes[:3]
		}
		return nil, nil, &TooLargeError{Entries: entries, Bytes: total, Largest: sizes}
	}
	kept := set.Files[:0]
	for _, f := range set.Files {
		if f.Path != "" {
			kept = append(kept, f)
		}
	}
	set.Files = kept
	return set, skipped, nil
}

var errGrew = errors.New("file grew while it was read")

var errNotRegular = errors.New("not a regular file")

// readCapped reads p unless it grew past limit. O_NONBLOCK keeps a FIFO a
// container swapped in after the walk from blocking the open.
func readCapped(root *os.Root, p string, limit int64) ([]byte, error) {
	f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errGrew
	}
	return data, nil
}
