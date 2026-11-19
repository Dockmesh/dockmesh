package migration

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// maxManifestEntries bounds the files of one copied volume; the server
// holds both manifests in memory while it compares them.
var maxManifestEntries = 1_000_000

// errTooManyEntries refuses a volume with more files than a copy checks.
var errTooManyEntries = fmt.Errorf("the volume has more than %d files, more than a migration checks; move it yourself", maxManifestEntries)

// manifest describes a volume archive: per path (hashed, so a volume's
// file names never reach an error message and every entry has the same
// size) the hash of what a copy must keep of it. Times are left out: the
// copy is checked for content, owners, modes and the extended attributes
// an archive carries.
type manifest struct {
	entries map[[sha256.Size]byte][sha256.Size]byte
	bytes   int64
}

// readManifest reads a tar.gz volume archive to its end. progress, when
// set, gets the file bytes read so far.
func readManifest(r io.Reader, progress func(int64)) (*manifest, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	m := &manifest{entries: map[[sha256.Size]byte][sha256.Size]byte{}}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		if len(m.entries) >= maxManifestEntries {
			return nil, errTooManyEntries
		}
		key := sha256.Sum256([]byte(entryName(hdr.Name)))
		if hdr.Typeflag == tar.TypeLink {
			// Which name of a hard-linked file carries the data depends on
			// the order tar walks the directory, which differs per host.
			target, ok := m.entries[sha256.Sum256([]byte(entryName(hdr.Linkname)))]
			if !ok {
				return nil, errors.New("archive: hard link to a file not in the archive")
			}
			m.entries[key] = target
			continue
		}
		h := sha256.New()
		typ := hdr.Typeflag
		if typ == tar.TypeRegA { //nolint:staticcheck // old archives still write TypeRegA
			typ = tar.TypeReg
		}
		var fields [8]int64
		fields[0], fields[1], fields[2], fields[3] = int64(typ), hdr.Mode&0o7777, int64(hdr.Uid), int64(hdr.Gid)
		fields[4], fields[5] = hdr.Devmajor, hdr.Devminor
		if typ == tar.TypeReg {
			n, err := io.Copy(h, tr) //nolint:gosec // streamed into a hash, never held in memory
			if err != nil {
				return nil, fmt.Errorf("archive: %w", err)
			}
			fields[6] = n
			m.bytes += n
			if progress != nil {
				progress(m.bytes)
			}
		}
		for _, f := range fields {
			_ = binary.Write(h, binary.BigEndian, f)
		}
		if typ == tar.TypeSymlink {
			h.Write([]byte(hdr.Linkname))
		}
		writeXattrs(h, hdr.PAXRecords)
		var sum [sha256.Size]byte
		copy(sum[:], h.Sum(nil))
		m.entries[key] = sum
	}
	// The gzip trailer holds the checksum of everything above.
	if _, err := io.Copy(io.Discard, zr); err != nil { //nolint:gosec // discarded, never held in memory
		return nil, fmt.Errorf("archive: %w", err)
	}
	return m, nil
}

// writeXattrs adds the entry's extended attributes (ACLs, file
// capabilities), in a fixed order.
func writeXattrs(w io.Writer, pax map[string]string) {
	var keys []string
	for k := range pax {
		if strings.HasPrefix(k, "SCHILY.xattr.") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = io.WriteString(w, k+"\x00"+pax[k]+"\x00")
	}
}

func entryName(name string) string {
	return path.Clean(strings.TrimPrefix(name, "./"))
}

// diff says how the copy c differs from m, nil when they match.
func (m *manifest) diff(c *manifest) error {
	missing, changed := 0, 0
	for k, want := range m.entries {
		got, ok := c.entries[k]
		switch {
		case !ok:
			missing++
		case got != want:
			changed++
		}
	}
	extra := 0
	for k := range c.entries {
		if _, ok := m.entries[k]; !ok {
			extra++
		}
	}
	if missing+changed+extra == 0 {
		return nil
	}
	return fmt.Errorf("of %d entries, %d missing, %d with other contents, owner or mode, %d extra", len(m.entries), missing, changed, extra)
}
