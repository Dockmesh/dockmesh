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
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
)

// volumeTarSource is the minimum capability hosttar needs — a way to
// open a tar stream for one named volume. Satisfied by the executor's
// hostBackupTarget (via either local docker or a remote agent).
type volumeTarSource interface {
	VolumeTar(ctx context.Context, name string) (io.ReadCloser, error)
}

// tarStackWithVolumes writes a single gzipped tar containing both the
// stack directory AND every named docker volume referenced by the
// stack's compose file. Layout:
//
//	dockmesh-backup.json      (manifest: the volumes expected below)
//	stack/...                 (the stack directory with owners, modes and times)
//	volumes/<vol>.tar.gz      (each volume as a nested tar.gz stream)
func tarStackWithVolumes(ctx context.Context, vts volumeTarSource, files *stackhelper.Helper, stackDir, stackName string, w io.Writer) (int64, error) {
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

	uid := os.Getuid()
	manifest, err := json.Marshal(stackManifest{Format: 1, Stack: stackName, Volumes: volumeNames, OwnerUID: &uid})
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
	walkErr := files.Walk(ctx, filepath.Dir(stackDir), stackDir, func(hdr *tar.Header, r io.Reader) error {
		hdr.Name = "stack/" + hdr.Name
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if r == nil {
			return nil
		}
		n, err := io.Copy(tw, r)
		total += n
		if err != nil {
			return err
		}
		// A file that shrank since it was opened is padded to the size
		// its header already promised.
		if n < hdr.Size {
			_, err = tw.Write(make([]byte, hdr.Size-n))
		}
		return err
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return total, walkErr
	}

	// --- Each volume -> volumes/<name>.tar.gz ----------------------------
	// Buffered on disk one at a time for the outer tar header's size.
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
	// OwnerUID is the uid the server ran as. Restore writes entries of
	// other owners through the helper so they keep their owner; archives
	// without it restore every file as the server's own.
	OwnerUID *int `json:"owner_uid,omitempty"`
}

func readStackManifest(r io.Reader) (*stackManifest, error) {
	var m stackManifest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", stackManifestName, err)
	}
	return &m, nil
}
