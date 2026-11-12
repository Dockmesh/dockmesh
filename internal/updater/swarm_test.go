package updater

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/client"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
)

var swarmLabels = map[string]string{
	LabelSwarmServiceID:        "x1y2z3",
	LabelSwarmServiceName:      "web",
	"com.docker.swarm.task.id": "t1",
}

func TestSwarmService(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"task container", swarmLabels, "web"},
		{"name label missing", map[string]string{LabelSwarmServiceID: "x1y2z3"}, "x1y2z3"},
		{"name without id", map[string]string{LabelSwarmServiceName: "web"}, ""},
		{"compose container", map[string]string{"com.docker.compose.project": "app"}, ""},
		{"no labels", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SwarmService(tt.labels); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// swarmDocker answers container inspects from containers (id → labels),
// lists them as running, and fails everything else. It records the
// paths it was asked for.
type swarmDocker struct {
	mu    sync.Mutex
	paths []string
}

func newSwarmDocker(t *testing.T, containers map[string]map[string]string) (*client.Client, *swarmDocker) {
	t.Helper()
	f := &swarmDocker{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		path := r.URL.Path
		if i := strings.Index(path, "/containers/"); i >= 0 {
			path = path[i:]
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/containers/json":
			var list []map[string]any
			for id, labels := range containers {
				list = append(list, map[string]any{"Id": id, "Image": id + "-image:1", "ImageID": "sha256:" + id, "Labels": labels, "State": "running"})
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
			labels, ok := containers[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"no such container"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": id, "Name": "/" + id, "Image": "sha256:" + id,
				"Config":     map[string]any{"Image": id + "-image:1", "Labels": labels},
				"HostConfig": map[string]any{},
			})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"not faked"}`))
		}
	}))
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli, f
}

func (f *swarmDocker) asked(part string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if strings.Contains(p, part) {
			return true
		}
	}
	return false
}

// Update and rollback refuse a Swarm task container before they pull or
// swap anything; other containers go on as before.
func TestUpdateAndRollbackRefuseSwarmTasks(t *testing.T) {
	cli, fake := newSwarmDocker(t, map[string]map[string]string{
		"task":  swarmLabels,
		"plain": {"maintainer": "me"},
	})
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	svc := NewService(docker.Wrap(cli), database)

	tests := []struct {
		name      string
		container string
		run       func(id string) error
		wantSwarm bool
		wantErr   error
	}{
		{"update task", "task", func(id string) error { _, err := svc.Update(context.Background(), id, ""); return err }, true, nil},
		{"rollback task", "task", func(id string) error { _, err := svc.Rollback(context.Background(), id, 1, ""); return err }, true, nil},
		{"update plain", "plain", func(id string) error { _, err := svc.Update(context.Background(), id, ""); return err }, false, nil},
		{"rollback plain", "plain", func(id string) error { _, err := svc.Rollback(context.Background(), id, 1, ""); return err }, false, ErrHistoryNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(tt.container)
			got, isSwarm := AsSwarmManaged(err)
			if isSwarm != tt.wantSwarm {
				t.Fatalf("err = %v, swarm managed = %v, want %v", err, isSwarm, tt.wantSwarm)
			}
			if tt.wantSwarm {
				if got != "web" || err.Error() != "Managed by Swarm service web." {
					t.Fatalf("service %q, message %q", got, err.Error())
				}
				return
			}
			if err == nil {
				t.Fatal("expected the fake daemon to fail the update")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
	if !fake.asked("/images/create") {
		t.Error("the plain container's update never reached the pull")
	}
}

// The watcher never checks images for Swarm task containers, so they are
// never offered as an update.
func TestWatcherSkipsSwarmTasks(t *testing.T) {
	cli, fake := newSwarmDocker(t, map[string]map[string]string{
		"task":  swarmLabels,
		"plain": nil,
	})
	w := NewWatcher(docker.Wrap(cli), nil, 0)
	w.poll(context.Background())
	if _, ok := w.Get("task-image:1"); ok {
		t.Error("swarm task image was checked")
	}
	if _, ok := w.Get("plain-image:1"); !ok {
		t.Error("plain container's image was not checked")
	}
	if fake.asked("sha256:task") {
		t.Error("watcher inspected the swarm task's image")
	}
}
