package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/updater"
)

var swarmTaskLabels = map[string]string{
	updater.LabelSwarmServiceID:   "x1y2z3",
	updater.LabelSwarmServiceName: "web",
}

// swarmGuardEnv is an authz env whose Docker daemon knows a Swarm task
// container "task", a second one "task2" and a plain container "plain",
// and fails every other call.
func swarmGuardEnv(t *testing.T) (*authzEnv, context.Context) {
	t.Helper()
	containers := map[string]map[string]string{
		"task":  swarmTaskLabels,
		"task2": swarmTaskLabels,
		"plain": nil,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if i := strings.Index(path, "/containers/"); i >= 0 && strings.HasSuffix(path, "/json") {
			id := strings.TrimSuffix(path[i+len("/containers/"):], "/json")
			if labels, ok := containers[id]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id": id, "Name": "/" + id, "Image": "sha256:" + id,
					"Config":     map[string]any{"Image": "nginx:1", "Labels": labels},
					"HostConfig": map[string]any{},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such container"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"not faked"}`))
	}))
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	dc := docker.Wrap(cli)
	t.Cleanup(func() { _ = dc.Close() })

	e := newAuthzEnv(t)
	e.h.Docker = dc
	e.h.Updater = updater.NewService(dc, e.db)
	e.h.Operations = operations.New(e.db)
	u := e.user("root", "admin")
	return e, middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})
}

func swarmGuardCall(ctx context.Context, handler http.HandlerFunc, id string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// Update and rollback answer 409 swarm_managed for a Swarm task container.
// A plain container gets past the guard; its status then comes from the
// fake daemon failing the pull (500) or from the missing history row (404).
func TestUpdateAndRollbackRefuseSwarmTask(t *testing.T) {
	e, ctx := swarmGuardEnv(t)
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		container string
		body      any
		want      int
		wantSwarm bool
	}{
		{"update task", e.h.UpdateContainer, "task", nil, http.StatusConflict, true},
		{"rollback task", e.h.RollbackContainer, "task", rollbackRequest{HistoryID: 1}, http.StatusConflict, true},
		{"update plain", e.h.UpdateContainer, "plain", nil, http.StatusInternalServerError, false},
		{"rollback plain", e.h.RollbackContainer, "plain", rollbackRequest{HistoryID: 1}, http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := swarmGuardCall(ctx, tt.handler, tt.container, tt.body)
			if rr.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			var got swarmSkip
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if (got.Code == codeSwarmManaged) != tt.wantSwarm {
				t.Fatalf("code %q: %s", got.Code, rr.Body)
			}
			if tt.wantSwarm && (got.Service != "web" || got.Error != "Managed by Swarm service web.") {
				t.Fatalf("body %s", rr.Body)
			}
		})
	}
}

// A bulk update with a mix queues only the non-Swarm containers and lists
// the others as skipped; one with only Swarm task containers is refused.
func TestBulkUpdateSkipsSwarmTasks(t *testing.T) {
	e, ctx := swarmGuardEnv(t)
	tests := []struct {
		name        string
		ids         []string
		want        int
		wantQueued  []string
		wantSkipped []string
	}{
		{"mix", []string{"task", "plain", "task2"}, http.StatusAccepted, []string{"plain"}, []string{"task", "task2"}},
		{"only swarm", []string{"task", "task2"}, http.StatusConflict, nil, []string{"task", "task2"}},
		{"only plain", []string{"plain"}, http.StatusAccepted, []string{"plain"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := swarmGuardCall(ctx, e.h.BulkUpdateContainers, "", bulkUpdateRequest{ContainerIDs: tt.ids})
			if rr.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			var got struct {
				Code       string          `json:"code"`
				Operations []operationView `json:"operations"`
				Skipped    []swarmSkip     `json:"skipped"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, op := range got.Operations {
				waitOpDone(t, ctx, e.h.Operations, op.ID)
			}
			if len(got.Operations) != len(tt.wantQueued) {
				t.Fatalf("%d operations queued, want %v", len(got.Operations), tt.wantQueued)
			}
			for i, op := range got.Operations {
				if op.Ref != "container:"+tt.wantQueued[i] {
					t.Errorf("queued %s, want %s", op.Ref, tt.wantQueued[i])
				}
			}
			if len(got.Skipped) != len(tt.wantSkipped) {
				t.Fatalf("skipped %+v, want %v", got.Skipped, tt.wantSkipped)
			}
			for i, s := range got.Skipped {
				if s.ContainerID != tt.wantSkipped[i] || s.Code != codeSwarmManaged || s.Service != "web" {
					t.Errorf("skipped[%d] = %+v", i, s)
				}
			}
			if tt.want == http.StatusConflict && got.Code != codeSwarmManaged {
				t.Errorf("code %q", got.Code)
			}
		})
	}
}

// waitOpDone lets the bulk queue's goroutine finish before the test's
// database is closed.
func waitOpDone(t *testing.T, ctx context.Context, ops *operations.Service, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := ops.Get(ctx, id); err == nil && !got.Status.Active() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s still active", id)
}

// Container rows of Swarm tasks carry their service and never the
// update-available flag.
func TestContainerRowsMarkSwarmTasks(t *testing.T) {
	h := &Handlers{}
	rows := []containerRow{
		{Summary: container.Summary{ID: "task", Labels: swarmTaskLabels}},
		{Summary: container.Summary{ID: "plain"}},
	}
	h.applyImageUpdateFlags(rows)
	if rows[0].SwarmService != "web" || rows[1].SwarmService != "" {
		t.Fatalf("swarm service %q / %q", rows[0].SwarmService, rows[1].SwarmService)
	}
	if rows[0].ImageUpdateAvailable {
		t.Fatal("swarm task offered as update available")
	}
}
