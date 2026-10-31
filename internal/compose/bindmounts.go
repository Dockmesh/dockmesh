package compose

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"
)

// RebaseBindSources points bind mounts that compose resolved into the
// temporary staging directory at the persistent stack directory instead.
// Compose resolves "./data" against the directory it was loaded from; the
// staging copy is deleted after the deploy, so without this the container
// would write into a directory that no longer exists.
// A bind of the staging directory itself (".:/app") becomes a bind of the
// stack directory, as docker compose would mount it.
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

// MountsProjectDir reports whether a service bind-mounts the project
// directory itself writable (".:/app"), which lets it rewrite compose.yaml
// and .env.
func MountsProjectDir(composeYAML string) bool {
	var doc struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal([]byte(composeYAML), &doc) != nil {
		return false
	}
	for _, svc := range doc.Services {
		for _, v := range svc.Volumes {
			var src string
			switch x := v.(type) {
			case string:
				parts := strings.Split(x, ":")
				if len(parts) < 2 || (len(parts) > 2 && strings.Contains(","+parts[2]+",", ",ro,")) {
					continue
				}
				src = parts[0]
			case map[string]any:
				if t, _ := x["type"].(string); t != "bind" {
					continue
				}
				if ro, _ := x["read_only"].(bool); ro {
					continue
				}
				src, _ = x["source"].(string)
			}
			if src != "" && path.Clean(filepath.ToSlash(src)) == "." {
				return true
			}
		}
	}
	return false
}

// stagingPathPattern matches the staging directories earlier versions
// deployed from: <tmp>/dockmesh-deploy-<stack>-<n> on the server and
// <data dir>/staging/<stack>-<n> on agents (os.MkdirTemp appends digits).
var stagingPathPattern = regexp.MustCompile(`/dockmesh-deploy-[^/]+|/staging/[a-z0-9][a-z0-9-]*-[0-9]+(/|$)`)

// IsStagingPath reports whether a bind source points into such a
// staging directory.
func IsStagingPath(src string) bool {
	return stagingPathPattern.MatchString(filepath.ToSlash(src))
}

// ErrStagedMounts refuses scale and rolling updates while the stack still
// runs with bind mounts into a deleted staging directory: replacing those
// containers would drop the data only they still hold.
var ErrStagedMounts = errors.New("this stack still uses a bind mount from a temporary directory; redeploy it first so its data is moved to the stack directory")

// BindCopy reports what happened to data left in an old staging mount.
type BindCopy struct {
	Service string `json:"service"`
	Target  string `json:"target"`
	Dest    string `json:"dest"`
	Files   int    `json:"files"`
	Error   string `json:"error,omitempty"`
}

type stagedMount struct {
	containerID, running, service, target, dest string
}

// stagedMounts lists bind mounts of the project's managed containers that
// point into an old staging directory, with the source the project now
// uses for the same target.
func (s *Service) stagedMounts(ctx context.Context, cli *client.Client, proj *composetypes.Project) ([]stagedMount, error) {
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", LabelManaged+"=true"),
			filters.Arg("label", "com.docker.compose.project="+proj.Name),
		),
	})
	if err != nil {
		return nil, err
	}
	var out []stagedMount
	for _, c := range list {
		if c.Labels["com.docker.compose.project"] != proj.Name {
			continue
		}
		svcName := c.Labels["com.docker.compose.service"]
		svc := proj.Services[svcName]
		for _, m := range c.Mounts {
			if m.Type != "bind" || !IsStagingPath(m.Source) {
				continue
			}
			sm := stagedMount{containerID: c.ID, running: c.State, service: svcName, target: m.Destination}
			for _, v := range svc.Volumes {
				if v.Type == composetypes.VolumeTypeBind && v.Target == m.Destination && !IsStagingPath(v.Source) {
					sm.dest = v.Source
				}
			}
			out = append(out, sm)
		}
	}
	return out, nil
}

func (s *Service) refuseStagedMounts(ctx context.Context, proj *composetypes.Project) error {
	staged, err := s.stagedMounts(ctx, s.docker.Raw(), proj)
	if err != nil {
		return err
	}
	if len(staged) > 0 {
		return ErrStagedMounts
	}
	return nil
}

// adoptStagedBindData moves what running containers wrote into a bind
// mount whose source was a (now deleted) staging directory into the
// persistent source the new containers will use. That data only exists
// inside the old container, so each one is stopped for a consistent copy
// and read through the Docker archive API. Nothing is copied when the new
// source already has content. On any failure the stopped containers are
// started again and the deploy must not continue.
func (s *Service) adoptStagedBindData(ctx context.Context, cli *client.Client, proj *composetypes.Project) ([]BindCopy, error) {
	staged, err := s.stagedMounts(ctx, cli, proj)
	if err != nil {
		return nil, err
	}
	var (
		out     []BindCopy
		stopped []string
	)
	restart := func() {
		for _, id := range stopped {
			_ = cli.ContainerStart(context.WithoutCancel(ctx), id, container.StartOptions{})
		}
	}
	for _, m := range staged {
		if m.dest == "" || !emptyOrMissing(m.dest) {
			continue
		}
		if m.running == "running" && !contains(stopped, m.containerID) {
			if err := cli.ContainerStop(ctx, m.containerID, container.StopOptions{}); err != nil {
				restart()
				return out, fmt.Errorf("stop %s before copying its data: %w", m.service, err)
			}
			stopped = append(stopped, m.containerID)
		}
		bc := BindCopy{Service: m.service, Target: m.target, Dest: m.dest}
		bc.Files, err = copyBindData(ctx, cli, m.containerID, m.target, m.dest)
		if err != nil {
			bc.Error = err.Error()
			out = append(out, bc)
			restart()
			return out, fmt.Errorf("copy data of %s:%s to %s: %w", m.service, m.target, m.dest, err)
		}
		slog.Info("copied data from old staging bind mount", "stack", proj.Name, "service", m.service,
			"target", m.target, "dest", m.dest, "files", bc.Files)
		out = append(out, bc)
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func emptyOrMissing(dir string) bool {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

const bindCopyHelperImage = "busybox:1.38"

// copyBindData streams path out of the old container into a helper
// container that has dest bind-mounted. The Docker daemon extracts as root
// and keeps the files' owners and modes, which a database directory needs;
// the helper's mount namespace keeps the archive inside dest.
func copyBindData(ctx context.Context, cli *client.Client, containerID, srcPath, dest string) (int, error) {
	if _, err := cli.ImageInspect(ctx, bindCopyHelperImage); err != nil {
		rc, err := cli.ImagePull(ctx, bindCopyHelperImage, image.PullOptions{})
		if err != nil {
			return 0, fmt.Errorf("pull helper image: %w", err)
		}
		_, _ = io.Copy(io.Discard, rc)
		rc.Close()
	}
	helper, err := cli.ContainerCreate(ctx, &container.Config{
		Image:  bindCopyHelperImage,
		Cmd:    []string{"true"},
		Labels: map[string]string{LabelManaged: "true", "dockmesh.component": "bind-copy-helper"},
	}, &container.HostConfig{Binds: []string{dest + ":/dest"}}, nil, nil, "")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), helper.ID, container.RemoveOptions{Force: true})
	}()

	rc, _, err := cli.CopyFromContainer(ctx, containerID, srcPath)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	pr, pw := io.Pipe()
	files := 0
	go func() {
		n, err := rewriteBindTar(tar.NewReader(rc), tar.NewWriter(pw), path.Base(srcPath))
		files = n
		pw.CloseWithError(err)
	}()
	if err := cli.CopyToContainer(ctx, helper.ID, "/dest", pr, container.CopyToContainerOptions{CopyUIDGID: true}); err != nil {
		pr.CloseWithError(err)
		return files, err
	}
	return files, nil
}

// rewriteBindTar copies directories and regular files from tr to tw with
// the leading prefix directory removed. Links and special files are left
// out, and names that would climb with ".." are refused.
func rewriteBindTar(tr *tar.Reader, tw *tar.Writer, prefix string) (int, error) {
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, tw.Close()
		}
		if err != nil {
			return files, err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(hdr.Name), prefix), "/")
		if rel == "" || rel == "." {
			continue
		}
		clean := path.Clean(rel)
		if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return files, fmt.Errorf("archive entry %q leaves the mount", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeReg:
		default:
			continue
		}
		out := *hdr
		out.Name = clean
		if hdr.Typeflag == tar.TypeDir {
			out.Name += "/"
		}
		out.Mode = hdr.Mode & 0o777
		if err := tw.WriteHeader(&out); err != nil {
			return files, err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil { //nolint:gosec // size bounded by the container's own data
				return files, err
			}
			files++
		}
	}
}
