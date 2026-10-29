package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
)

// volumeTarSource is the minimum capability hosttar needs — a way to
// open a tar stream for one named volume. Satisfied by the executor's
// hostBackupTarget (via either local docker or a remote agent).
type volumeTarSource interface {
	VolumeTar(ctx context.Context, name string) (io.ReadCloser, error)
}

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
//	dockmesh-backup.json      (manifest: the volumes expected below)
//	stack/...                 (compose.yaml + .env + .dockmesh.meta.json)
//	volumes/<vol>.tar.gz      (each volume as a nested tar.gz stream)
func tarStackWithVolumes(ctx context.Context, vts volumeTarSource, stackDir, stackName string, w io.Writer) (int64, error) {
	// Without the compose file there is no way to tell which volumes
	// belong to the stack, and an archive without them would look like a
	// complete backup.
	envContent, err := os.ReadFile(filepath.Join(stackDir, ".env"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read .env: %w", err)
	}
	proj, err := compose.LoadProject(ctx, stackDir, stackName, string(envContent))
	if err != nil {
		return 0, fmt.Errorf("parse compose to find the stack's volumes: %w", err)
	}
	var volumeNames []string
	for key, v := range proj.Volumes {
		// Default compose name is <stack>_<key>; respect explicit name.
		name := v.Name
		if name == "" {
			name = stackName + "_" + key
		}
		volumeNames = append(volumeNames, name)
	}
	sort.Strings(volumeNames)
	if len(volumeNames) > 0 && vts == nil {
		return 0, errors.New("stack has volumes but no host to read them from")
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	var total int64

	manifest, err := json.Marshal(stackManifest{Format: 1, Stack: stackName, Volumes: volumeNames})
	if err != nil {
		return 0, err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: stackManifestName, Mode: 0o600, Size: int64(len(manifest)),
		ModTime: time.Now(), Format: tar.FormatPAX,
	}); err != nil {
		return 0, err
	}
	if _, err := tw.Write(manifest); err != nil {
		return 0, err
	}

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
	// Buffered on disk one at a time for the outer tar header's size. Any
	// volume that can't be read fails the backup.
	for _, vn := range volumeNames {
		n, err := appendVolume(ctx, vts, tw, vn)
		total += n
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return total, fmt.Errorf("volume %s: %w", vn, err)
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

func appendVolume(ctx context.Context, vts volumeTarSource, tw *tar.Writer, name string) (int64, error) {
	rc, err := vts.VolumeTar(ctx, name)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp("", "dm-volbak-*")
	if err != nil {
		_ = rc.Close()
		return 0, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	n, copyErr := io.Copy(tmp, rc)
	closeErr := rc.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:    fmt.Sprintf("volumes/%s.tar.gz", name),
		Mode:    0o600,
		Size:    n,
		ModTime: time.Now(),
		Format:  tar.FormatPAX,
	}); err != nil {
		return 0, err
	}
	return io.CopyN(tw, tmp, n)
}

// stackManifestName is the first entry of a stack archive. It records the
// volumes the stack had at backup time, so verify can tell a complete
// archive from one that lost a volume.
const stackManifestName = "dockmesh-backup.json"

type stackManifest struct {
	Format  int      `json:"format"`
	Stack   string   `json:"stack"`
	Volumes []string `json:"volumes"`
}

func readStackManifest(r io.Reader) (*stackManifest, error) {
	var m stackManifest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", stackManifestName, err)
	}
	return &m, nil
}
