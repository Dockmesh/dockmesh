package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
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

// tarStackWithVolumes writes a single gzipped tar containing both the
// stack directory AND every named docker volume referenced by the
// stack's compose file. Layout:
//
//	stack/...                 (compose.yaml + .env + .dockmesh.meta.json)
//	volumes/<vol>.tar.gz      (each volume as a nested tar.gz stream)
//
// A future restore path can untar `stack/` back into the stacks dir
// and per-volume restore each `volumes/<vol>.tar.gz` into the
// recreated named volume. Fixes FINDING-32 (stack backup was
// compose-only — worthless for disaster recovery).
func tarStackWithVolumes(ctx context.Context, dc *docker.Client, stackDir, stackName string, w io.Writer) (int64, error) {
	// Try to load the compose to discover referenced volumes. If parsing
	// fails we fall through to a stack-dir-only archive — better to save
	// what we can than fail the whole backup.
	var volumeNames []string
	envContent, _ := os.ReadFile(filepath.Join(stackDir, ".env"))
	proj, err := compose.LoadProject(ctx, stackDir, stackName, string(envContent))
	if err == nil {
		for key, v := range proj.Volumes {
			// Default compose name is <stack>_<key>; respect explicit name.
			name := v.Name
			if name == "" {
				name = stackName + "_" + key
			}
			volumeNames = append(volumeNames, name)
		}
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	var total int64

	// --- Stack dir -> stack/<rel> ----------------------------------------
	walkErr := filepath.WalkDir(stackDir, func(p string, d fs.DirEntry, err error) error {
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
		rel, err := filepath.Rel(stackDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr.Name = filepath.ToSlash(filepath.Join("stack", rel))
		if d.IsDir() {
			return tw.WriteHeader(hdr)
		}
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
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return total, walkErr
	}

	// --- Each volume -> volumes/<name>.tar.gz ----------------------------
	// Buffer each volume stream on disk so we have a known size for the
	// outer tar header. Cleanup on return.
	for _, vn := range volumeNames {
		if dc == nil {
			continue
		}
		rc, err := tarVolume(ctx, dc, vn)
		if err != nil {
			// Volume might legitimately not exist yet (stack never
			// deployed). Skip quietly; stack config is still captured.
			continue
		}
		tmp, err := os.CreateTemp("", "dm-volbak-*")
		if err != nil {
			_ = rc.Close()
			continue
		}
		n, copyErr := io.Copy(tmp, rc)
		_ = rc.Close()
		if copyErr != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			continue
		}
		if _, err := tmp.Seek(0, 0); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			continue
		}
		hdr := &tar.Header{
			Name:    fmt.Sprintf("volumes/%s.tar.gz", vn),
			Mode:    0o600,
			Size:    n,
			ModTime: time.Now(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			_ = tw.Close()
			_ = gz.Close()
			return total, err
		}
		m, copyErr := io.CopyN(tw, tmp, n)
		total += m
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		if copyErr != nil && copyErr != io.EOF {
			_ = tw.Close()
			_ = gz.Close()
			return total, copyErr
		}
	}

	if err := tw.Close(); err != nil {
		return total, err
	}
	if err := gz.Close(); err != nil {
		return total, err
	}
	return total, nil
}
