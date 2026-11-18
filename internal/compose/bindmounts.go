package compose

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"gopkg.in/yaml.v3"
)

// stagingDepth is how far below its temporary base a staging directory
// sits. A relative path that climbs out of the stack directory ("../x")
// then still resolves inside the base, where it can be told apart from an
// absolute path.
const stagingDepth = 8

// NewStagingDir creates an empty directory to stage a compose project in,
// stagingDepth levels below a new temporary directory in parent ("" for
// the system's). cleanup removes all of it.
func NewStagingDir(parent, pattern string) (string, func(), error) {
	base, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(base) }
	parts := []string{base}
	for i := 1; i < stagingDepth; i++ {
		parts = append(parts, "s")
	}
	dir := filepath.Join(append(parts, "stack")...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dir, cleanup, nil
}

// RebaseStagedPaths points the paths compose resolved against the staging
// directory at the persistent stack directory instead: bind sources,
// config and secret files and build contexts. Compose resolves "./data"
// against the directory it was loaded from, and the staging copy is
// deleted after the deploy. A bind of the staging directory itself
// (".:/app") becomes a bind of the stack directory, and "../x" lands next
// to it, as docker compose would mount them. Paths that leave the stack
// directory are returned as warnings naming where they now point.
func RebaseStagedPaths(proj *composetypes.Project, stagingDir, stackDir string) []string {
	stagingDir = filepath.Clean(stagingDir)
	base := stagingBase(stagingDir)
	var warnings []string
	rebase := func(p, what string) string {
		if !filepath.IsAbs(p) {
			return p
		}
		src := filepath.Clean(p)
		if !within(base, src) {
			return p
		}
		rel, err := filepath.Rel(stagingDir, src)
		if err != nil {
			return p
		}
		out := filepath.Join(stackDir, rel)
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			warnings = append(warnings, fmt.Sprintf("%s %s points outside the stack directory, to %s", what, filepath.ToSlash(rel), out))
		}
		return out
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Services)) {
		svc := proj.Services[name]
		for i, v := range svc.Volumes {
			if v.Type == composetypes.VolumeTypeBind {
				svc.Volumes[i].Source = rebase(v.Source, "service "+name+": bind")
			}
		}
		if svc.Build != nil {
			svc.Build.Context = rebase(svc.Build.Context, "service "+name+": build context")
			svc.Build.Dockerfile = rebase(svc.Build.Dockerfile, "service "+name+": dockerfile")
			for k, c := range svc.Build.AdditionalContexts {
				svc.Build.AdditionalContexts[k] = rebase(c, "service "+name+": build context "+k)
			}
		}
		proj.Services[name] = svc
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Configs)) {
		c := proj.Configs[name]
		c.File = rebase(c.File, "config "+name+": file")
		proj.Configs[name] = c
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Secrets)) {
		c := proj.Secrets[name]
		c.File = rebase(c.File, "secret "+name+": file")
		proj.Secrets[name] = c
	}
	return warnings
}

// stagingBase is the temporary base NewStagingDir put dir in, or dir
// itself for a directory it didn't create: then only paths inside dir
// are rebased, never an absolute path that merely shares a parent.
func stagingBase(dir string) string {
	if filepath.Base(dir) != "stack" {
		return dir
	}
	base := filepath.Dir(dir)
	for i := 1; i < stagingDepth; i++ {
		if filepath.Base(base) != "s" {
			return dir
		}
		base = filepath.Dir(base)
	}
	return base
}

func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// StackDirOnHost is a stack's directory on one host. Resolve, when set,
// turns a path on that host into its real path; it is only set for the
// server's own host, where the paths can be looked at.
type StackDirOnHost struct {
	Path    string
	Resolve func(string) string
}

// WritableStackDirMounts lists the read-write bind mounts in the compose
// files that let a service rewrite the files Dockmesh deploys from: a bind
// of the stack directory itself, of a directory above it, or of one of
// those files, also through a named volume that is a host bind. Sources
// with variables are not followed.
func WritableStackDirMounts(composeFiles []string, dirs []StackDirOnHost) []stacks.StackDirMount {
	resolved := make([]StackDirOnHost, len(dirs))
	for i, d := range dirs {
		resolved[i] = StackDirOnHost{Path: realPath(d.Path, d.Resolve), Resolve: d.Resolve}
	}
	type composeDoc struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]struct {
			DriverOpts map[string]string `yaml:"driver_opts"`
		} `yaml:"volumes"`
	}
	// Top-level volumes merge across the files, later ones winning, as
	// compose merges them.
	docs := make([]composeDoc, 0, len(composeFiles))
	hostBinds := map[string]string{}
	for _, file := range composeFiles {
		var doc composeDoc
		if yaml.Unmarshal([]byte(file), &doc) != nil {
			continue
		}
		docs = append(docs, doc)
		for name, v := range doc.Volumes {
			if strings.Contains(","+v.DriverOpts["o"]+",", ",bind,") && v.DriverOpts["device"] != "" {
				hostBinds[name] = v.DriverOpts["device"]
			} else {
				delete(hostBinds, name)
			}
		}
	}
	var out []stacks.StackDirMount
	seen := map[stacks.StackDirMount]bool{}
	for _, doc := range docs {
		for _, name := range slices.Sorted(maps.Keys(doc.Services)) {
			for _, v := range doc.Services[name].Volumes {
				src, target, ok := writableBind(v, hostBinds)
				if !ok || strings.Contains(src, "$") || strings.HasPrefix(src, "~") {
					continue
				}
				for _, dir := range resolved {
					m := stacks.StackDirMount{Service: name, Source: src, Target: target}
					if bindReachesStackFiles(src, dir) && !seen[m] {
						seen[m] = true
						out = append(out, m)
						break
					}
				}
			}
		}
	}
	return out
}

// writableBind returns source and target of a read-write bind entry in
// short or long syntax; hostBinds maps named volumes that are host binds
// to their device path.
func writableBind(v any, hostBinds map[string]string) (string, string, bool) {
	switch x := v.(type) {
	case string:
		parts := strings.Split(x, ":")
		if len(parts) < 2 || (len(parts) > 2 && strings.Contains(","+parts[2]+",", ",ro,")) {
			return "", "", false
		}
		if dev, ok := hostBinds[parts[0]]; ok {
			return dev, parts[1], true
		}
		if !strings.HasPrefix(parts[0], ".") && !strings.HasPrefix(parts[0], "/") {
			return "", "", false // a named volume
		}
		return parts[0], parts[1], true
	case map[string]any:
		if ro, _ := x["read_only"].(bool); ro {
			return "", "", false
		}
		src, _ := x["source"].(string)
		target, _ := x["target"].(string)
		switch t, _ := x["type"].(string); t {
		case "bind":
			return src, target, src != ""
		case "volume":
			dev, ok := hostBinds[src]
			return dev, target, ok
		}
	}
	return "", "", false
}

func realPath(p string, resolve func(string) string) string {
	if resolve != nil {
		p = resolve(p)
	}
	return path.Clean(filepath.ToSlash(p))
}

// bindReachesStackFiles reports whether a bind source is the stack
// directory dir (already resolved), a directory above it, or a file a
// deploy reads directly inside it. The source is resolved on dir's host,
// so a symlink in or to the stack directory is followed there.
func bindReachesStackFiles(src string, dir StackDirOnHost) bool {
	p := filepath.FromSlash(src)
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.FromSlash(dir.Path), p)
	}
	rp := realPath(p, dir.Resolve)
	switch {
	case rp == dir.Path, rp == "/", strings.HasPrefix(dir.Path, rp+"/"):
		return true
	case path.Dir(rp) == dir.Path:
		return isDeployedFile(path.Base(rp))
	}
	return false
}

// isDeployedFile reports whether name, directly in a stack directory, is
// a file a deploy reads: the compose files and overrides, .env and its
// encrypted form, and Dockmesh's .dockmesh* files.
func isDeployedFile(name string) bool {
	name = strings.ToLower(name)
	return name == ".env" || name == ".env.age" || strings.HasPrefix(name, ".dockmesh") ||
		deployedComposeFile.MatchString(name)
}

var deployedComposeFile = regexp.MustCompile(`^(docker-)?compose(\.[^/]+)?\.ya?ml$`)

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
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", LabelManaged+"=true").Add("label", "com.docker.compose.project="+proj.Name),
	})
	if err != nil {
		return nil, err
	}
	var out []stagedMount
	for _, c := range list.Items {
		if c.Labels["com.docker.compose.project"] != proj.Name {
			continue
		}
		svcName := c.Labels["com.docker.compose.service"]
		svc := proj.Services[svcName]
		for _, m := range c.Mounts {
			if m.Type != "bind" || !IsStagingPath(m.Source) {
				continue
			}
			sm := stagedMount{containerID: c.ID, running: string(c.State), service: svcName, target: m.Destination}
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
			_, _ = cli.ContainerStart(context.WithoutCancel(ctx), id, client.ContainerStartOptions{})
		}
	}
	for _, m := range staged {
		if m.dest == "" || !emptyOrMissing(m.dest) {
			continue
		}
		if m.running == "running" && !contains(stopped, m.containerID) {
			if _, err := cli.ContainerStop(ctx, m.containerID, client.ContainerStopOptions{}); err != nil {
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

const bindCopyHelperImage = docker.BusyboxHelperImage

// copyBindData streams path out of the old container into a helper
// container that has dest bind-mounted. The Docker daemon extracts as root
// and keeps the files' owners and modes, which a database directory needs;
// the helper's mount namespace keeps the archive inside dest.
func copyBindData(ctx context.Context, cli *client.Client, containerID, srcPath, dest string) (int, error) {
	if _, err := cli.ImageInspect(ctx, bindCopyHelperImage); err != nil {
		if err := docker.PullAnonymous(ctx, cli, bindCopyHelperImage); err != nil {
			return 0, fmt.Errorf("pull helper image: %w", err)
		}
	}
	helper, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  bindCopyHelperImage,
			Cmd:    []string{"true"},
			Labels: map[string]string{LabelManaged: "true", "dockmesh.component": "bind-copy-helper"},
		},
		HostConfig: &container.HostConfig{Binds: []string{dest + ":/dest"}},
	})
	if err != nil {
		return 0, err
	}
	defer func() {
		_, _ = cli.ContainerRemove(context.WithoutCancel(ctx), helper.ID, client.ContainerRemoveOptions{Force: true})
	}()

	src, err := cli.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: srcPath})
	if err != nil {
		return 0, err
	}
	defer src.Content.Close()
	pr, pw := io.Pipe()
	files := 0
	go func() {
		n, err := rewriteBindTar(tar.NewReader(src.Content), tar.NewWriter(pw), path.Base(srcPath))
		files = n
		pw.CloseWithError(err)
	}()
	if _, err := cli.CopyToContainer(ctx, helper.ID, client.CopyToContainerOptions{DestinationPath: "/dest", Content: pr, CopyUIDGID: true}); err != nil {
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
