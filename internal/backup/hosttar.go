package backup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tarHostDir writes a gzipped tar of dir into w. Used to back up stack
// directories on the host filesystem (compose.yaml, .env, .env.age,
// .dockmesh.meta.json, …). Volumes that the stack references are NOT
// included — the user backs those up via separate volume sources so we
// keep restore semantics simple.
func tarHostDir(dir string, w io.Writer) (int64, error) {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if hdr.Name == "." {
			return nil
		}
		if d.IsDir() {
			return tw.WriteHeader(hdr)
		}
		// Lock in size via the open fd — same race as tarSystem: walker
		// saw stat-A, but a live writer can grow the file before we
		// stream bytes, and tar errors out with "write too long". Use
		// the at-open-time size; any later growth is ignored.
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		hdr.Size = st.Size()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		n, copyErr := io.CopyN(tw, f, st.Size())
		total += n
		if copyErr != nil && copyErr != io.EOF {
			return copyErr
		}
		if n < st.Size() {
			pad := make([]byte, st.Size()-n)
			if _, err := tw.Write(pad); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return total, err
	}
	if err := tw.Close(); err != nil {
		return total, err
	}
	if err := gz.Close(); err != nil {
		return total, err
	}
	_ = strings.Trim // keep imported
	return total, nil
}
