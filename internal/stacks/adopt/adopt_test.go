package adopt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"

	dtypes "github.com/docker/docker/api/types"
)

// --- test doubles -----------------------------------------------------------

type fakeHost struct {
	id         string
	name       string
	containers []dtypes.Container
	listErr    error
}

func (f *fakeHost) ID() string   { return f.id }
func (f *fakeHost) Name() string { return f.name }
func (f *fakeHost) ListContainers(_ context.Context, _ bool) ([]dtypes.Container, error) {
	return f.containers, f.listErr
}

type fakeHostSource struct {
	byID map[string]*fakeHost
}

func (f *fakeHostSource) Pick(id string) (ContainerLister, error) {
	h, ok := f.byID[id]
	if !ok {
		return nil, errors.New("no such host")
	}
	return h, nil
}
func (f *fakeHostSource) List(context.Context) ([]host.Info, error) {
	out := make([]host.Info, 0, len(f.byID))
	for id, h := range f.byID {
		out = append(out, host.Info{ID: id, Name: h.name, Kind: "local", Status: "online"})
	}
	return out, nil
}

type fakeStacks struct {
	existing map[string]bool
	created  map[string]createCall
	dir      string
}

type createCall struct {
	compose string
	env     string
}

func (f *fakeStacks) Has(name string) bool { return f.existing[name] }
func (f *fakeStacks) Create(name, compose, env string) (*stacks.Detail, error) {
	if f.existing[name] {
		return nil, stacks.ErrExists
	}
	if f.created == nil {
		f.created = map[string]createCall{}
	}
	f.created[name] = createCall{compose: compose, env: env}
	// Mimic the manager behaviour: creating the stack also creates the
	// directory on disk (so adopt can merge the bundle in).
	dir := filepath.Join(f.dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		return nil, err
	}
	return &stacks.Detail{Name: name, Compose: compose, Env: env}, nil
}
func (f *fakeStacks) Dir(name string) (string, error) {
	return filepath.Join(f.dir, name), nil
}

// --- Discover tests ---------------------------------------------------------

func TestDiscoverGroupsByProjectLabelAndSkipsManaged(t *testing.T) {
	ctx := context.Background()
	src := &fakeHostSource{byID: map[string]*fakeHost{
		"local": {
			id:   "local",
			name: "mac-mini",
			containers: []dtypes.Container{
				{
					ID: "c1", Names: []string{"/audiobookshelf"}, State: "running",
					Image: "ghcr.io/advplyr/audiobookshelf:latest", Created: 1_700_000_000,
					Labels: map[string]string{
						"com.docker.compose.project": "audiobookshelf",
						"com.docker.compose.service": "audiobookshelf",
					},
				},
				{
					ID: "c2", Names: []string{"/audnexus"}, State: "running",
					Image: "audnexus:local", Created: 1_700_000_050,
					Labels: map[string]string{
						"com.docker.compose.project": "audiobookshelf",
						"com.docker.compose.service": "audnexus",
					},
				},
				{
					ID: "c3", Names: []string{"/already_managed"}, State: "running",
					Labels: map[string]string{
						"com.docker.compose.project": "already-managed",
						"com.docker.compose.service": "web",
					},
				},
				{
					ID: "c4", Names: []string{"/ad-hoc"}, State: "running",
					// no project label — must be ignored
				},
			},
		},
	}}
	svc := &Service{Hosts: src, Stacks: &fakeStacks{existing: map[string]bool{"already-managed": true}}}

	got, err := svc.Discover(ctx, "local")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 discovered, got %d: %+v", len(got), got)
	}
	ds := got[0]
	if ds.ProjectName != "audiobookshelf" {
		t.Errorf("project = %q, want audiobookshelf", ds.ProjectName)
	}
	if ds.ServiceCount != 2 {
		t.Errorf("service_count = %d, want 2", ds.ServiceCount)
	}
	if ds.HostName != "mac-mini" {
		t.Errorf("host_name = %q, want mac-mini", ds.HostName)
	}
	// Services sorted alphabetically: audiobookshelf, audnexus.
	if len(ds.Services) != 2 || ds.Services[0].Name != "audiobookshelf" || ds.Services[1].Name != "audnexus" {
		t.Errorf("services not sorted: %+v", ds.Services)
	}
	if ds.FirstSeen.IsZero() {
		t.Error("first_seen was zero — should be earliest container time")
	}
}

func TestDiscoverHostUnknown(t *testing.T) {
	src := &fakeHostSource{byID: map[string]*fakeHost{}}
	svc := &Service{Hosts: src, Stacks: &fakeStacks{}}
	_, err := svc.Discover(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error for unknown host")
	}
}

// --- Adopt tests ------------------------------------------------------------

const minimalCompose = `services:
  web:
    image: nginx:alpine
`

func newAdoptHarness(t *testing.T) (*Service, *fakeStacks, string) {
	t.Helper()
	tmp := t.TempDir()
	st := &fakeStacks{existing: map[string]bool{}, dir: tmp}
	src := &fakeHostSource{byID: map[string]*fakeHost{
		"local": {
			id: "local", name: "mac-mini",
			containers: []dtypes.Container{
				{
					ID: "c1", State: "running",
					Labels: map[string]string{
						"com.docker.compose.project": "myapp",
						"com.docker.compose.service": "web",
					},
				},
			},
		},
	}}
	return &Service{Hosts: src, Stacks: st}, st, tmp
}

func TestAdoptWritesComposeAndBindsContainers(t *testing.T) {
	svc, st, root := newAdoptHarness(t)
	res, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "myapp", HostID: "local", Compose: minimalCompose,
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if res.BoundContainers != 1 {
		t.Errorf("bound = %d, want 1", res.BoundContainers)
	}
	if got, ok := st.created["myapp"]; !ok {
		t.Error("stacks.Create was not called")
	} else if !strings.Contains(got.compose, "nginx:alpine") {
		t.Errorf("compose persisted incorrectly: %q", got.compose)
	}
	if _, err := os.Stat(filepath.Join(root, "myapp", "compose.yaml")); err != nil {
		t.Errorf("compose.yaml not on disk: %v", err)
	}
}

func TestAdoptRejectsDuplicateStackName(t *testing.T) {
	svc, st, _ := newAdoptHarness(t)
	st.existing["myapp"] = true
	_, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "myapp", HostID: "local", Compose: minimalCompose,
	})
	if !errors.Is(err, ErrAlreadyManaged) {
		t.Errorf("want ErrAlreadyManaged, got %v", err)
	}
}

func TestAdoptRejectsWhenNoMatchingContainers(t *testing.T) {
	svc, _, _ := newAdoptHarness(t)
	_, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "ghost", HostID: "local", Compose: minimalCompose,
	})
	if !errors.Is(err, ErrNoRunning) {
		t.Errorf("want ErrNoRunning, got %v", err)
	}
}

func TestAdoptMergesBundleIntoStackDir(t *testing.T) {
	svc, _, root := newAdoptHarness(t)
	bundle := mustTarGz(t, map[string]string{
		"nginx.conf":            "server { listen 80; }\n",
		"certs/ca.pem":          "pem-bytes",
		"compose.yaml":          "DIFFERENT — must be ignored in favour of request body",
		"build/Dockerfile.prod": "FROM node:20\n",
	})
	_, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "myapp", HostID: "local", Compose: minimalCompose, Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("Adopt with bundle: %v", err)
	}
	// Expected files
	for _, rel := range []string{"nginx.conf", "certs/ca.pem", "build/Dockerfile.prod"} {
		if _, err := os.Stat(filepath.Join(root, "myapp", rel)); err != nil {
			t.Errorf("bundle file %q missing: %v", rel, err)
		}
	}
	// compose.yaml should be the one from the request, not from the bundle.
	got, _ := os.ReadFile(filepath.Join(root, "myapp", "compose.yaml"))
	if !strings.Contains(string(got), "nginx:alpine") {
		t.Errorf("compose.yaml taken from bundle instead of request: %q", got)
	}
}

func TestAdoptRejectsBundleWithPathTraversal(t *testing.T) {
	svc, _, _ := newAdoptHarness(t)
	bundle := mustTarGz(t, map[string]string{
		"../escape.sh": "rm -rf /\n",
	})
	_, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "myapp", HostID: "local", Compose: minimalCompose, Bundle: bundle,
	})
	if !errors.Is(err, ErrBundleUnsafe) {
		t.Errorf("want ErrBundleUnsafe, got %v", err)
	}
}

func TestAdoptRejectsBundleExceedingSizeLimit(t *testing.T) {
	svc, _, _ := newAdoptHarness(t)
	svc.MaxSize = 256 // very small — easy to trip
	big := strings.Repeat("x", 1024)
	bundle := mustTarGz(t, map[string]string{"big.txt": big})
	_, err := svc.Adopt(context.Background(), AdoptRequest{
		Name: "myapp", HostID: "local", Compose: minimalCompose, Bundle: bundle,
	})
	if !errors.Is(err, ErrBundleTooLarge) {
		t.Errorf("want ErrBundleTooLarge, got %v", err)
	}
}

// --- test helpers -----------------------------------------------------------

func mustTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Size:     int64(len(content)),
			Mode:     0o644,
		}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
