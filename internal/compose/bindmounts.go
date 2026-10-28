package compose

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/dockmesh/dockmesh/internal/safepath"
)

// RebaseBindSources points bind mounts that compose resolved into the
// temporary staging directory at the persistent stack directory instead.
// Compose resolves "./data" against the directory it was loaded from; the
// staging copy is deleted after the deploy, so without this the container
// would write into a directory that no longer exists.
func RebaseBindSources(proj *composetypes.Project, stagingDir, stackDir string) {
	stagingDir = filepath.Clean(stagingDir)
	for name, svc := range proj.Services {
		changed := false
		for i, v := range svc.Volumes {
			if v.Type != composetypes.VolumeTypeBind {
				continue
			}
			src := filepath.Clean(v.Source)
			if src != stagingDir && !strings.HasPrefix(src, stagingDir+string(filepath.Separator)) {
				continue
			}
			rel, err := filepath.Rel(stagingDir, src)
			if err != nil {
				continue
			}
			svc.Volumes[i].Source = filepath.Join(stackDir, rel)
			changed = true
		}
		if changed {
			proj.Services[name] = svc
		}
	}
}

// IsStagingPath reports whether a bind source points into a deploy
// staging directory of an earlier version: the server used
// <tmp>/dockmesh-deploy-<stack>-*, agents <data>/staging/<stack>-*.
func IsStagingPath(src string) bool {
	s := filepath.ToSlash(src)
	return strings.Contains(s, "/dockmesh-deploy-") || strings.Contains(s, "/dockmesh/staging/")
}

// BindCopy reports what happened to data left in an old staging mount.
type BindCopy struct {
	Service string `json:"service"`
	Target  string `json:"target"`
	Dest    string `json:"dest"`
	Files   int    `json:"files"`
	Error   string `json:"error,omitempty"`
}

// adoptStagedBindData copies what a running container wrote into a bind
// mount whose source was a (now deleted) staging directory into the
// persistent source the new container will use. The data only exists
// inside the old container, so it is read through the Docker archive API.
// Nothing is copied when the new source already has content.
func (s *Service) adoptStagedBindData(ctx context.Context, proj *composetypes.Project) []BindCopy {
	cli := s.docker.Raw()
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+proj.Name)),
	})
	if err != nil {
		return nil
	}
	var out []BindCopy
	for _, c := range list {
		svcName := c.Labels["com.docker.compose.service"]
		svc, ok := proj.Services[svcName]
		if !ok {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type != "bind" || !IsStagingPath(m.Source) {
				continue
			}
			dest := ""
			for _, v := range svc.Volumes {
				if v.Type == composetypes.VolumeTypeBind && v.Target == m.Destination {
					dest = v.Source
				}
			}
			if dest == "" || IsStagingPath(dest) {
				continue
			}
			bc := BindCopy{Service: svcName, Target: m.Destination, Dest: dest}
			if !emptyOrMissing(dest) {
				continue
			}
			n, err := copyFromContainer(ctx, s, c.ID, m.Destination, dest)
			bc.Files = n
			if err != nil {
				bc.Error = err.Error()
				slog.Warn("bind mount data copy failed", "stack", proj.Name, "service", svcName, "target", m.Destination, "err", err)
			} else {
				slog.Info("copied data from old staging bind mount", "stack", proj.Name, "service", svcName,
					"target", m.Destination, "dest", dest, "files", n)
			}
			out = append(out, bc)
		}
	}
	return out
}

func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

// copyFromContainer extracts path from the container into dest. Only
// directories and regular files are written, each through safepath so the
// archive can't place anything outside dest.
func copyFromContainer(ctx context.Context, s *Service, containerID, path, dest string) (int, error) {
	rc, _, err := s.docker.Raw().CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	// The archive's top entry is the directory itself (base name of path).
	return extractInto(tar.NewReader(rc), filepath.Base(path), dest)
}

// extractInto writes the archive's directories and regular files below
// dest, dropping the leading prefix directory.
func extractInto(tr *tar.Reader, prefix, dest string) (int, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return files, err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(hdr.Name), prefix), "/")
		if rel == "" {
			continue
		}
		target, err := safepath.Resolve(dest, rel)
		if err != nil {
			return files, fmt.Errorf("%s: %w", hdr.Name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hdr.Mode&0o777)) //nolint:gosec // masked to permission bits
			if err != nil {
				return files, err
			}
			_, err = io.Copy(f, tr) //nolint:gosec // size bounded by the container's own data
			f.Close()
			if err != nil {
				return files, err
			}
			if hdr.Uid != 0 || hdr.Gid != 0 {
				_ = os.Lchown(target, hdr.Uid, hdr.Gid)
			}
			files++
		}
	}
}
