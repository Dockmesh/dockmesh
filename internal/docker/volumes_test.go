package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
)

func TestCheckVolumeName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"pgdata", true},
		{"fx-postgres-srv_pgdata", true},
		{"a.b-c_d", true},
		{"/", false},
		{"/etc", false},
		{"/var/run/docker.sock", false},
		{"./data", false},
		{"../x", false},
		{"vol:/x", false},
		{"x", false},
		{"", false},
		{"-leading", false},
	}
	for _, tt := range tests {
		err := CheckVolumeName(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidVolumeName) {
			t.Errorf("%q: error not ErrInvalidVolumeName", tt.name)
		}
	}
}

type fakePruner struct {
	api     string
	client  string
	listed  []*volume.Volume
	removed []string
	pruned  []filters.Args
}

func (f *fakePruner) ServerVersion(context.Context) (types.Version, error) {
	return types.Version{APIVersion: f.api}, nil
}

func (f *fakePruner) ClientVersion() string {
	if f.client == "" {
		return f.api
	}
	return f.client
}

func (f *fakePruner) VolumesPrune(_ context.Context, a filters.Args) (volume.PruneReport, error) {
	f.pruned = append(f.pruned, a)
	return volume.PruneReport{}, nil
}

func (f *fakePruner) VolumeList(context.Context, volume.ListOptions) (volume.ListResponse, error) {
	return volume.ListResponse{Volumes: f.listed}, nil
}

func (f *fakePruner) VolumeRemove(_ context.Context, id string, _ bool) error {
	f.removed = append(f.removed, id)
	return nil
}

func TestPruneUnusedVolumesExcludesOwnVolumes(t *testing.T) {
	f := &fakePruner{api: "1.43"}
	if _, err := PruneUnusedVolumes(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(f.pruned) != 1 {
		t.Fatalf("prune calls = %d", len(f.pruned))
	}
	if !f.pruned[0].ExactMatch("label!", LabelManaged) {
		t.Fatalf("filters = %v, want the managed label excluded", f.pruned[0])
	}
	if f.pruned[0].Contains("all") {
		t.Fatal("prune must not ask for all volumes")
	}
}

// Before API 1.42 Docker's prune also deletes named volumes.
func TestPruneUnusedVolumesOldDaemonKeepsNamed(t *testing.T) {
	anon := strings.Repeat("a", 64)
	ownAnon := strings.Repeat("b", 64)
	plugin := strings.Repeat("c", 64)
	f := &fakePruner{api: "1.41", listed: []*volume.Volume{
		{Name: anon, Driver: "local"},
		{Name: plugin, Driver: "nfs-plugin"},
		{Name: ownAnon, Driver: "local", Labels: map[string]string{LabelManaged: "true"}},
		{Name: "dockmesh-caddy-data"},
		{Name: "dockmesh-caddy-config"},
		{Name: "stack_pgdata"},
	}}
	report, err := PruneUnusedVolumes(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.pruned) != 0 {
		t.Fatal("old daemon must not get Docker's prune")
	}
	if len(f.removed) != 1 || f.removed[0] != anon || len(report.VolumesDeleted) != 1 {
		t.Fatalf("removed = %v, report = %+v", f.removed, report)
	}
}

// The daemon adds all=true for a request below API 1.42, whatever it
// supports itself.
func TestPruneUnusedVolumesPinnedClientKeepsNamed(t *testing.T) {
	f := &fakePruner{api: "1.43", client: "1.41", listed: []*volume.Volume{{Name: "stack_pgdata", Driver: "local"}}}
	if _, err := PruneUnusedVolumes(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(f.pruned) != 0 || len(f.removed) != 0 {
		t.Fatalf("pruned = %v, removed = %v", f.pruned, f.removed)
	}
}
