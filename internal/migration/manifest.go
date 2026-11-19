package migration

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// manifestEntry is what a copied volume must keep of one archive entry.
// Times are left out: the copy is checked for content, owners and modes.
type manifestEntry struct {
	typ      byte
	mode     int64
	uid, gid int
	size     int64
	sum      [sha256.Size]byte
	link     string
}

// manifest describes a volume archive by path.
type manifest struct {
	entries map[string]manifestEntry
	bytes   int64
}

// readManifest reads a tar.gz volume archive to its end. progress, when
// set, gets the file bytes read so far.
func readManifest(r io.Reader, progress func(int64)) (*manifest, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	m := &manifest{entries: map[string]manifestEntry{}}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		name := entryName(hdr.Name)
		e := manifestEntry{typ: hdr.Typeflag, mode: hdr.Mode & 0o7777, uid: hdr.Uid, gid: hdr.Gid}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // old archives still write TypeRegA
			e.typ = tar.TypeReg
			h := sha256.New()
			n, err := io.Copy(h, tr) //nolint:gosec // streamed into a hash, never held in memory
			if err != nil {
				return nil, fmt.Errorf("archive %s: %w", name, err)
			}
			e.size = n
			copy(e.sum[:], h.Sum(nil))
			m.bytes += n
			if progress != nil {
				progress(m.bytes)
			}
		case tar.TypeLink:
			// Which name of a hard-linked file carries the data depends on
			// the order tar walks the directory, which differs per host.
			target, ok := m.entries[entryName(hdr.Linkname)]
			if !ok {
				return nil, fmt.Errorf("archive %s: hard link to missing %s", name, hdr.Linkname)
			}
			e = target
		case tar.TypeSymlink:
			e.link = hdr.Linkname
		}
		m.entries[name] = e
	}
	// The gzip trailer holds the checksum of everything above.
	if _, err := io.Copy(io.Discard, zr); err != nil { //nolint:gosec // discarded, never held in memory
		return nil, fmt.Errorf("archive: %w", err)
	}
	return m, nil
}

func entryName(name string) string {
	return path.Clean(strings.TrimPrefix(name, "./"))
}

// diff describes the first difference between m and the copy c, nil when
// they match.
func (m *manifest) diff(c *manifest) error {
	var names []string
	for n := range m.entries {
		names = append(names, n)
	}
	for n := range c.entries {
		if _, ok := m.entries[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		want, inSource := m.entries[n]
		got, inCopy := c.entries[n]
		switch {
		case !inCopy:
			return fmt.Errorf("%s is missing in the copy", n)
		case !inSource:
			return fmt.Errorf("%s is in the copy but not in the source", n)
		case want.typ != got.typ:
			return fmt.Errorf("%s changed its type", n)
		case want.uid != got.uid || want.gid != got.gid:
			return fmt.Errorf("%s is owned by %d:%d instead of %d:%d", n, got.uid, got.gid, want.uid, want.gid)
		case want.mode != got.mode:
			return fmt.Errorf("%s has mode %04o instead of %04o", n, got.mode, want.mode)
		case want.size != got.size || want.sum != got.sum:
			return fmt.Errorf("%s has different contents", n)
		case want.link != got.link:
			return fmt.Errorf("%s points to %s instead of %s", n, got.link, want.link)
		}
	}
	return nil
}
