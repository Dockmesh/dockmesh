package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/client"
)

type fakeContainer struct {
	id, name string
	labels   map[string]string
	env      []string
	restart  string
}

// fakeDaemon answers the container calls a deploy makes, enough to watch
// which replicas it removes and creates.
type fakeDaemon struct {
	mu      sync.Mutex
	next    int
	byID    map[string]*fakeContainer
	removed []string
}

func newFakeDaemon() *fakeDaemon { return &fakeDaemon{byID: map[string]*fakeContainer{}} }

func (d *fakeDaemon) add(name string, labels map[string]string, env ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.next++
	id := fmt.Sprintf("%064d", d.next)
	d.byID[id] = &fakeContainer{id: id, name: name, labels: labels, env: env}
}

func (d *fakeDaemon) find(ref string) *fakeContainer {
	if c, ok := d.byID[ref]; ok {
		return c
	}
	for _, c := range d.byID {
		if c.name == ref {
			return c
		}
	}
	return nil
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := r.URL.Path
	if i := strings.Index(path, "/containers/"); i >= 0 {
		path = path[i+len("/containers/"):]
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && path == "json":
		out := []map[string]any{}
		for _, c := range d.byID {
			out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Labels": c.labels, "State": "running"})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && path == "create":
		var body struct {
			Env        []string
			Labels     map[string]string
			HostConfig struct{ RestartPolicy struct{ Name string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := r.URL.Query().Get("name")
		if d.find(name) != nil {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"name in use"}`))
			return
		}
		d.next++
		id := fmt.Sprintf("%064d", d.next)
		d.byID[id] = &fakeContainer{id: id, name: name, labels: body.Labels, env: body.Env, restart: body.HostConfig.RestartPolicy.Name}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"` + id + `"}`))
	case r.Method == http.MethodDelete:
		c := d.find(path)
		if c == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such container"}`))
			return
		}
		d.removed = append(d.removed, c.name)
		delete(d.byID, c.id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && (strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/start")):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
		c := d.find(strings.TrimSuffix(path, "/json"))
		if c == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such container"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": c.id, "Name": "/" + c.name,
			"State":  map[string]any{"Running": true, "Status": "running", "Health": map[string]any{"Status": "healthy"}},
			"Config": map[string]any{"Labels": c.labels, "Env": c.env},
		})
	default:
		http.NotFound(w, r)
	}
}

// replicas returns name → env of every container of service web.
func (d *fakeDaemon) replicas() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string][]string{}
	for _, c := range d.byID {
		if c.labels[LabelService] == "web" {
			out[c.name] = c.env
		}
	}
	return out
}

func fakeClient(t *testing.T, h http.Handler) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestDeployRenewsEveryReplica(t *testing.T) {
	webLabels := map[string]string{LabelProject: "demo", LabelService: "web"}
	tests := []struct {
		name          string
		existing      []string
		containerName string
		want          []string
		wantRemoved   []string
	}{
		{name: "three replicas", existing: []string{"demo-web-1", "demo-web-2", "demo-web-3"},
			want: []string{"demo-web-1", "demo-web-2", "demo-web-3"}, wantRemoved: []string{"demo-web-1", "demo-web-2", "demo-web-3"}},
		{name: "first deploy", want: []string{"demo-web-1"}},
		{name: "gap in the numbers", existing: []string{"demo-web-1", "demo-web-3"},
			want: []string{"demo-web-1", "demo-web-2"}, wantRemoved: []string{"demo-web-1", "demo-web-3"}},
		{name: "container_name", existing: []string{"web"}, containerName: "web",
			want: []string{"web"}, wantRemoved: []string{"web"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newFakeDaemon()
			for _, n := range tt.existing {
				d.add(n, webLabels, "VERSION=old")
			}
			d.add("demo-db-1", map[string]string{LabelProject: "demo", LabelService: "db"}, "VERSION=old")
			cli := fakeClient(t, d)
			proj := &composetypes.Project{Name: "demo", Services: composetypes.Services{}}
			svc := composetypes.ServiceConfig{
				Name: "web", Image: "web:2", ContainerName: tt.containerName,
				Environment: composetypes.MappingWithEquals{"VERSION": strp("new")},
				HealthCheck: &composetypes.HealthCheckConfig{Test: []string{"CMD", "true"}},
			}
			proj.Services["web"] = svc

			res, err := (&Service{}).deployService(context.Background(), cli, proj, svc, nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			got := d.replicas()
			names := make([]string, 0, len(got))
			for n, env := range got {
				names = append(names, n)
				if !slices.Equal(env, []string{"VERSION=new"}) {
					t.Errorf("%s env = %v, want the new config", n, env)
				}
			}
			slices.Sort(names)
			if !slices.Equal(names, tt.want) {
				t.Errorf("replicas = %v, want %v", names, tt.want)
			}
			if !slices.Equal(d.removed, tt.wantRemoved) {
				t.Errorf("removed = %v, want %v", d.removed, tt.wantRemoved)
			}
			if d.find("demo-db-1") == nil {
				t.Error("container of another service removed")
			}
			if first := d.find(tt.want[0]); res.ContainerID != first.id {
				t.Errorf("result container = %s, want the first replica", res.ContainerID)
			}
			for _, n := range tt.want[1:] {
				if r := d.find(n).restart; r != "unless-stopped" {
					t.Errorf("%s restart policy = %q, want unless-stopped", n, r)
				}
			}
		})
	}
}

func TestIgnoredReplicas(t *testing.T) {
	one, three := 1, 3
	proj := &composetypes.Project{Services: composetypes.Services{
		"api":  {Name: "api", Deploy: &composetypes.DeployConfig{Replicas: &three}},
		"web":  {Name: "web", Deploy: &composetypes.DeployConfig{Replicas: &one}},
		"db":   {Name: "db"},
		"cron": {Name: "cron", Deploy: &composetypes.DeployConfig{}},
	}}
	if got := ignoredReplicas(proj); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("ignored = %v, want [api]", got)
	}
}
