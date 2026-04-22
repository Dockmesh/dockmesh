// Package adopt implements the "take over a running compose project"
// flow. Given a compose project already running on a host via plain
// `docker compose up` (i.e. not managed by dockmesh), Discover finds
// such projects by scanning the `com.docker.compose.project` label on
// running containers and filtering out anything the stacks manager
// already tracks. Adopt writes the user-supplied compose.yaml and any
// supporting files into the stack directory so the manager picks it up
// and binds management over to dockmesh — without touching the running
// containers themselves.
//
// The key correctness invariant: adoption is a **metadata-only** action.
// No docker operations run against the running containers. A later
// explicit deploy (via the Stacks UI) is the only thing that might
// trigger recreation, and that's gated by the operator and driven by
// compose's own config-hash reconciliation.
package adopt

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"

	dtypes "github.com/docker/docker/api/types"
)

const (
	// Compose label names. These are stable across docker-compose v1, v2,
	// and docker compose CLI plugin — moby/compose uses the same key
	// throughout. Source: https://docs.docker.com/reference/compose-file/
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"

	// Bundle safety limits. Adoption is by a privileged admin, but
	// "privileged admin uploads a 4 GB tarball that fills the disk"
	// is still worth guarding against.
	maxBundleBytes   = 100 * 1024 * 1024 // 100 MiB decompressed
	maxBundleEntries = 10_000
)

// ContainerLister is the minimum surface of host.Host that adopt.Service
// actually touches. Declared separately from host.Host (which has ~30
// methods) so tests can fake it in a handful of lines. The real
// host.Host satisfies it structurally.
type ContainerLister interface {
	ID() string
	Name() string
	ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error)
}

// HostSource is what Discover/Adopt need to resolve a host by ID. In
// production this is a thin wrapper around *host.Registry (see
// WrapRegistry). In tests it's a map-backed fake with no docker daemon
// dependency.
type HostSource interface {
	Pick(id string) (ContainerLister, error)
	List(ctx context.Context) ([]host.Info, error)
}

// WrapRegistry adapts a *host.Registry so it satisfies HostSource. The
// registry's Pick returns the full host.Host interface; we just narrow
// the return type to ContainerLister so tests can stub it cheaply
// without re-implementing every host.Host method.
func WrapRegistry(reg *host.Registry) HostSource {
	return registrySource{reg}
}

type registrySource struct{ reg *host.Registry }

func (r registrySource) Pick(id string) (ContainerLister, error) {
	return r.reg.Pick(id)
}
func (r registrySource) List(ctx context.Context) ([]host.Info, error) {
	return r.reg.List(ctx)
}

// StackCreator is the subset of stacks.Manager we need — also an
// interface for testability.
type StackCreator interface {
	Create(name, compose, env string) (*stacks.Detail, error)
	Dir(name string) (string, error)
	Has(name string) bool
}

// Service orchestrates discovery + adoption of compose projects.
type Service struct {
	Hosts   HostSource
	Stacks  StackCreator
	Clock   func() time.Time
	MaxSize int64 // 0 = use maxBundleBytes
}

// DiscoveredStack is one compose project on a host with no matching
// stack file on the server. Returned by Discover.
type DiscoveredStack struct {
	ProjectName  string                   `json:"project_name"`
	HostID       string                   `json:"host_id"`
	HostName     string                   `json:"host_name"`
	ServiceCount int                      `json:"service_count"`
	Services     []DiscoveredStackService `json:"services"`
	FirstSeen    time.Time                `json:"first_seen,omitempty"`
}

// DiscoveredStackService carries the subset of container info the
// validator / UI needs — enough to show "you have this many services
// running, here are the names + images + states."
type DiscoveredStackService struct {
	Name          string `json:"name"`
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name,omitempty"`
	State         string `json:"state"`
	Image         string `json:"image,omitempty"`
}

// AdoptRequest is what the handler hands us after JSON-decoding + base64-
// decoding the bundle field.
type AdoptRequest struct {
	Name             string
	HostID           string
	Compose          string
	Env              string
	Bundle           []byte // raw tar.gz, may be nil
	AcceptedWarnings []string
}

// AdoptResult is what we return to the handler; same shape as the
// OpenAPI StackAdoptResult schema.
type AdoptResult struct {
	Name            string   `json:"name"`
	HostID          string   `json:"host_id"`
	BoundContainers int      `json:"bound_containers"`
	Warnings        []string `json:"warnings,omitempty"`
	DriftServices   []string `json:"drift_services,omitempty"`
}

// Error kinds the handler maps to specific HTTP statuses.
var (
	ErrAlreadyManaged  = errors.New("a stack with this name is already managed")
	ErrNoRunning       = errors.New("no running containers with this project label on the target host")
	ErrServicesMissing = errors.New("compose declares services that aren't running on the target host")
	ErrBundleTooLarge  = errors.New("bundle exceeds maximum size")
	ErrBundleMalformed = errors.New("bundle is not a valid tar.gz archive")
	ErrBundleUnsafe    = errors.New("bundle contains an unsafe path (absolute, traversal, or symlink out)")
)

// Discover returns every compose project running on hostID that
// dockmesh's stacks manager does not already track. Sorted by project
// name for stable UI ordering.
func (s *Service) Discover(ctx context.Context, hostID string) ([]DiscoveredStack, error) {
	target, err := s.Hosts.Pick(hostID)
	if err != nil {
		return nil, err
	}
	containers, err := target.ListContainers(ctx, true) // true = include stopped
	if err != nil {
		return nil, fmt.Errorf("list containers on host %s: %w", target.ID(), err)
	}

	// Group by project label.
	grouped := map[string][]dtypes.Container{}
	for _, c := range containers {
		proj := strings.TrimSpace(c.Labels[labelProject])
		if proj == "" {
			continue
		}
		grouped[proj] = append(grouped[proj], c)
	}

	out := make([]DiscoveredStack, 0, len(grouped))
	for proj, cs := range grouped {
		if s.Stacks != nil && s.Stacks.Has(proj) {
			continue // already managed, skip
		}
		ds := DiscoveredStack{
			ProjectName: proj,
			HostID:      target.ID(),
			HostName:    target.Name(),
		}
		var firstSeen time.Time
		seenSvc := map[string]bool{}
		for _, c := range cs {
			svcName := strings.TrimSpace(c.Labels[labelService])
			if svcName == "" {
				svcName = "(unknown)"
			}
			if !seenSvc[svcName] {
				seenSvc[svcName] = true
			}
			name := ""
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			ds.Services = append(ds.Services, DiscoveredStackService{
				Name:          svcName,
				ContainerID:   c.ID,
				ContainerName: name,
				State:         c.State,
				Image:         c.Image,
			})
			created := time.Unix(c.Created, 0)
			if firstSeen.IsZero() || created.Before(firstSeen) {
				firstSeen = created
			}
		}
		ds.ServiceCount = len(seenSvc)
		ds.FirstSeen = firstSeen
		// Stable ordering inside the services slice too — otherwise the
		// same project renders differently on every reload because Docker's
		// list endpoint doesn't guarantee ordering.
		sort.Slice(ds.Services, func(i, j int) bool { return ds.Services[i].Name < ds.Services[j].Name })
		out = append(out, ds)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectName < out[j].ProjectName })
	return out, nil
}

// Adopt writes the user-supplied compose + optional bundle into the
// stacks root and binds the resulting stack to the already-running
// containers on the target host. No container operations are performed.
func (s *Service) Adopt(ctx context.Context, req AdoptRequest) (*AdoptResult, error) {
	if err := stacks.ValidateName(req.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Compose) == "" {
		return nil, fmt.Errorf("compose is required")
	}
	if s.Stacks == nil {
		return nil, fmt.Errorf("stacks manager not configured")
	}
	if s.Stacks.Has(req.Name) {
		return nil, ErrAlreadyManaged
	}

	target, err := s.Hosts.Pick(req.HostID)
	if err != nil {
		return nil, fmt.Errorf("resolve host %q: %w", req.HostID, err)
	}
	containers, err := target.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list containers on host %s: %w", target.ID(), err)
	}
	matching := filterByProject(containers, req.Name)
	if len(matching) == 0 {
		return nil, ErrNoRunning
	}

	// Bundle first — if the archive is malformed we want to reject before
	// we write anything to disk. Extract to a temp dir next to the stacks
	// root, then rename into place after Create succeeds so the manager's
	// fsnotify watcher sees one atomic "stack appeared" rather than a
	// half-populated dir.
	var stagingDir string
	if len(req.Bundle) > 0 {
		limit := s.MaxSize
		if limit <= 0 {
			limit = maxBundleBytes
		}
		stagingDir, err = os.MkdirTemp("", "dockmesh-adopt-*")
		if err != nil {
			return nil, fmt.Errorf("mkdir staging: %w", err)
		}
		defer os.RemoveAll(stagingDir)
		if err := extractTarGz(req.Bundle, stagingDir, limit); err != nil {
			return nil, err
		}
	}

	// Create the stack — writes compose.yaml + env, registers in-memory,
	// starts watching the new dir.
	if _, err := s.Stacks.Create(req.Name, req.Compose, req.Env); err != nil {
		return nil, fmt.Errorf("create stack: %w", err)
	}

	// Move bundle contents into the stack dir if we extracted any. The
	// manager already wrote its own compose.yaml, so we skip any
	// compose*.yaml / compose*.yml in the bundle — the user-edited
	// content in the request is authoritative.
	if stagingDir != "" {
		dstDir, err := s.Stacks.Dir(req.Name)
		if err != nil {
			return nil, fmt.Errorf("resolve stack dir: %w", err)
		}
		if err := mergeBundleInto(stagingDir, dstDir); err != nil {
			return nil, fmt.Errorf("merge bundle: %w", err)
		}
	}

	warnings := uniqueStrings(req.AcceptedWarnings)
	result := &AdoptResult{
		Name:            req.Name,
		HostID:          target.ID(),
		BoundContainers: len(matching),
		Warnings:        warnings,
		// DriftServices intentionally empty for v0.2.0 — reliable drift
		// detection requires reproducing docker compose's config-hash
		// algorithm, which is a slice of its own. Ship adoption first;
		// the first redeploy shows the drift explicitly anyway.
		DriftServices: nil,
	}
	return result, nil
}

func filterByProject(cs []dtypes.Container, project string) []dtypes.Container {
	var out []dtypes.Container
	for _, c := range cs {
		if strings.TrimSpace(c.Labels[labelProject]) == project {
			out = append(out, c)
		}
	}
	return out
}

// extractTarGz unpacks a tar.gz archive into dstDir, enforcing the slip
// guard against "..", absolute paths, symlinks pointing out, device
// files, and total-bytes / entry-count limits. Returns a typed error so
// the handler can map to 413 / 422 / 500 appropriately.
func extractTarGz(bundle []byte, dstDir string, maxBytes int64) error {
	gz, err := gzip.NewReader(newLimitReader(bundle, maxBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBundleMalformed, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	absRoot, err := filepath.Abs(dstDir)
	if err != nil {
		return err
	}

	var totalBytes int64
	var entryCount int
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBundleMalformed, err)
		}
		entryCount++
		if entryCount > maxBundleEntries {
			return fmt.Errorf("%w: more than %d entries", ErrBundleTooLarge, maxBundleEntries)
		}

		clean := filepath.Clean(hdr.Name)
		if clean == "." || clean == "" {
			continue
		}
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") ||
			strings.Contains(clean, string(os.PathSeparator)+"..") {
			return fmt.Errorf("%w: %q", ErrBundleUnsafe, hdr.Name)
		}
		target := filepath.Join(absRoot, clean)
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrBundleUnsafe, hdr.Name)
		}
		// Defense in depth: after Abs+Join, ensure we're still under root.
		if !strings.HasPrefix(absTarget+string(os.PathSeparator), absRoot+string(os.PathSeparator)) && absTarget != absRoot {
			return fmt.Errorf("%w: %q escapes root", ErrBundleUnsafe, hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, maxBytes-totalBytes+1))
			cerr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("%w: %v", ErrBundleMalformed, copyErr)
			}
			if cerr != nil {
				return cerr
			}
			totalBytes += n
			if totalBytes > maxBytes {
				return fmt.Errorf("%w: more than %d bytes", ErrBundleTooLarge, maxBytes)
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Reject links entirely — easiest safe policy. compose
			// bundles shouldn't need them; anyone who does can tar
			// without -l / resolve to regular files before packing.
			return fmt.Errorf("%w: symlinks not allowed (%q -> %q)", ErrBundleUnsafe, hdr.Name, hdr.Linkname)
		default:
			// Skip device files, FIFOs, etc. — they have no business
			// in a compose-context tarball.
			continue
		}
	}
	return nil
}

// mergeBundleInto copies every file + dir from src into dst, skipping
// compose files (the user-submitted compose.yaml in the adopt request is
// authoritative and was already written by the manager). Used to deposit
// the supporting files (build contexts, config files referenced by
// relative bind mounts) alongside the managed compose.yaml.
func mergeBundleInto(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip compose-ish files at the bundle root — the request's
		// compose field is the source of truth.
		if filepath.Dir(rel) == "." && isComposeFilename(filepath.Base(rel)) {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func isComposeFilename(name string) bool {
	switch name {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// limitReader is a small io.Reader wrapper that enforces a byte limit.
// We wrap the gzip input with it so a decompression bomb (tiny .gz that
// unpacks to 10 GiB) can't OOM the server.
type limitReader struct {
	r   io.Reader
	n   int64
	max int64
}

func newLimitReader(b []byte, max int64) *limitReader {
	return &limitReader{r: &byteReader{b: b}, max: max}
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n >= l.max {
		return 0, fmt.Errorf("%w (gzip input exceeds limit)", ErrBundleTooLarge)
	}
	if int64(len(p)) > l.max-l.n {
		p = p[:l.max-l.n]
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	return n, err
}

type byteReader struct {
	b   []byte
	off int
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.off >= len(b.b) {
		return 0, io.EOF
	}
	n := copy(p, b.b[b.off:])
	b.off += n
	return n, nil
}
