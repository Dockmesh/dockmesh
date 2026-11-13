package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type agentDeleteEnv struct {
	*authzEnv
	mgr   *stacks.Manager
	token string

	mu      sync.Mutex
	frames  []string // every frame type the agent got
	deletes []string
	fail    string // the agent answers every delete with this error
}

func newAgentDeleteEnv(t *testing.T) *agentDeleteEnv {
	t.Helper()
	e := &agentDeleteEnv{authzEnv: newScopeEnv(t)}
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	e.mgr = mgr
	e.h.Stacks = mgr
	e.h.Deployments = stacks.NewDeploymentStore(e.db)
	e.h.Notices = notices.NewService(e.db)
	e.h.Agents = agents.NewService(e.db, nil, "http://dockmesh.test", "wss://dockmesh.test/connect")
	if _, err := e.db.Exec(`INSERT INTO agents (id, name, status) VALUES ('agent-1', 'edge-1', 'offline')`); err != nil {
		t.Fatal(err)
	}
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksDelete)).Delete("/stacks/{name}", e.h.DeleteStack)
	})
	e.user("root", "admin")
	e.token = e.login("root").AccessToken
	return e
}

// connect attaches a fake agent that answers req.stack.delete as a real
// one does: OK, or OK=false with its error.
func (e *agentDeleteEnv) connect(t *testing.T) {
	t.Helper()
	ag, stop := agents.NewLoopback("agent-1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion},
		func(f agents.Frame, reply func(agents.Frame)) {
			e.mu.Lock()
			e.frames = append(e.frames, f.Type)
			e.mu.Unlock()
			if f.Type != agents.FrameReqStackDelete {
				payload, _ := json.Marshal(agents.ResponseEnvelope{OK: true})
				reply(agents.Frame{Type: agents.FrameRes, ID: f.ID, Payload: payload})
				return
			}
			var req agents.StackNameReq
			_ = json.Unmarshal(f.Payload, &req)
			e.mu.Lock()
			e.deletes = append(e.deletes, req.Name)
			env := agents.ResponseEnvelope{OK: e.fail == "", Error: e.fail}
			e.mu.Unlock()
			payload, _ := json.Marshal(env)
			reply(agents.Frame{Type: agents.FrameRes, ID: f.ID, Payload: payload})
		})
	detach := e.h.Agents.AttachLoopback(ag)
	t.Cleanup(func() { detach(); stop() })
}

func (e *agentDeleteEnv) stack(t *testing.T) {
	const name = "db"
	t.Helper()
	if _, err := e.mgr.Create(name, "services:\n  a:\n    image: busybox\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Deployments.Set(context.Background(), name, "agent-1", "deployed"); err != nil {
		t.Fatal(err)
	}
}

func (e *agentDeleteEnv) status(t *testing.T) string {
	const name = "db"
	t.Helper()
	d, err := e.h.Deployments.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		return ""
	}
	return d.Status
}

func (e *agentDeleteEnv) openNotices(t *testing.T) []notices.Notice {
	t.Helper()
	list, err := e.h.Notices.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestDeleteAgentStackWaitsForTheAgent(t *testing.T) {
	tests := []struct {
		name       string
		fail       string
		wantCode   int
		wantKept   bool
		wantStatus string
	}{
		{"agent deletes its copy", "", http.StatusNoContent, false, ""},
		{"agent error reaches the user", "remove pgdata: pull busybox: no route to host", http.StatusBadGateway, true, "deployed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAgentDeleteEnv(t)
			e.fail = tt.fail
			e.connect(t)
			e.stack(t)

			rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil)
			if rr.Code != tt.wantCode {
				t.Fatalf("delete: %d %s, want %d", rr.Code, rr.Body, tt.wantCode)
			}
			if tt.fail != "" && (!strings.Contains(rr.Body.String(), tt.fail) || !strings.Contains(rr.Body.String(), "edge-1")) {
				t.Errorf("response does not carry the agent's error and host: %s", rr.Body)
			}
			if got := e.mgr.DirExists("db"); got != tt.wantKept {
				t.Errorf("stack files kept on the server = %v, want %v", got, tt.wantKept)
			}
			if got := e.status(t); got != tt.wantStatus {
				t.Errorf("deployment status = %q, want %q", got, tt.wantStatus)
			}
			if len(e.deletes) != 1 || e.deletes[0] != "db" {
				t.Errorf("agent deletes = %v, want [db]", e.deletes)
			}
		})
	}
}

func TestDeleteAgentStackPendingUntilReconnect(t *testing.T) {
	tests := []struct {
		name       string
		fail       string
		redeploy   bool
		wantKept   bool
		wantStatus string
		wantNotice bool
	}{
		{"finished on reconnect", "", false, false, "", false},
		{"failure on reconnect raises a notice", "device or resource busy", false, true, stacks.DeploymentDeletePending, true},
		{"a deploy cancels the delete", "", true, true, "deployed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAgentDeleteEnv(t)
			e.stack(t)
			ctx := context.Background()

			rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil)
			if rr.Code != http.StatusAccepted || !strings.Contains(rr.Body.String(), `"pending_host_name":"edge-1"`) {
				t.Fatalf("delete with the agent offline: %d %s", rr.Code, rr.Body)
			}
			if !e.mgr.DirExists("db") || e.status(t) != stacks.DeploymentDeletePending {
				t.Fatalf("pending delete removed the stack or did not mark it: status %q", e.status(t))
			}
			if rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil); rr.Code != http.StatusAccepted {
				t.Fatalf("second delete while offline: %d %s", rr.Code, rr.Body)
			}
			if tt.redeploy {
				if err := e.h.Deployments.Set(ctx, "db", "agent-1", "deployed"); err != nil {
					t.Fatal(err)
				}
			}

			e.fail = tt.fail
			e.connect(t)
			e.h.ResumePendingStackDeletes(ctx, "agent-1")

			if got := e.mgr.DirExists("db"); got != tt.wantKept {
				t.Errorf("stack files kept = %v, want %v", got, tt.wantKept)
			}
			if got := e.status(t); got != tt.wantStatus {
				t.Errorf("deployment status = %q, want %q", got, tt.wantStatus)
			}
			if tt.redeploy && len(e.deletes) != 0 {
				t.Errorf("a redeployed stack was deleted on the agent: %v", e.deletes)
			}
			open := e.openNotices(t)
			if got := len(open) == 1 && strings.Contains(open[0].Body, tt.fail) && strings.Contains(open[0].Body, "edge-1"); got != tt.wantNotice {
				t.Errorf("notice with the agent error = %v, want %v: %+v", got, tt.wantNotice, open)
			}
		})
	}
}

// A failed attempt leaves the delete pending; the next connect that
// succeeds finishes it and resolves the notice.
func TestPendingStackDeleteRetriedOnNextConnect(t *testing.T) {
	e := newAgentDeleteEnv(t)
	e.stack(t)
	ctx := context.Background()
	if rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil); rr.Code != http.StatusAccepted {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	e.fail = "busy"
	e.connect(t)
	e.h.ResumePendingStackDeletes(ctx, "agent-1")
	if len(e.openNotices(t)) != 1 {
		t.Fatal("no notice after the failed attempt")
	}
	e.fail = ""
	e.h.ResumePendingStackDeletes(ctx, "agent-1")
	if e.mgr.DirExists("db") || e.status(t) != "" {
		t.Fatalf("stack not deleted on the second attempt: status %q", e.status(t))
	}
	if n := e.openNotices(t); len(n) != 0 {
		t.Errorf("notice not resolved: %+v", n)
	}
}

// A removed host never reconnects; its stacks are deleted on the server
// as before.
func TestDeleteStackOfRemovedAgent(t *testing.T) {
	e := newAgentDeleteEnv(t)
	e.stack(t)
	if _, err := e.db.Exec(`DELETE FROM agents WHERE id = 'agent-1'`); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	if e.mgr.DirExists("db") {
		t.Error("stack kept")
	}
}

func TestAgentErrorText(t *testing.T) {
	long := strings.Repeat("é", 400)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"prefix dropped", errors.New("agent: disk full"), "disk full"},
		{"control characters", errors.New("agent: a\nb\x1b[2Jc"), "a b [2Jc"},
		{"long text cut on a rune", errors.New("agent: " + long), long[:512] + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentErrorText(tt.err); got != tt.want {
				t.Errorf("agentErrorText = %q, want %q", got, tt.want)
			}
		})
	}
}

// The stop the user asked for with an offline host runs on reconnect,
// before the files go.
func TestPendingStackDeleteStopsContainersFirst(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"?stop=true", []string{agents.FrameReqStackStop, agents.FrameReqStackDelete}},
		{"", []string{agents.FrameReqStackDelete}},
	}
	for _, tt := range tests {
		t.Run("query "+tt.query, func(t *testing.T) {
			e := newAgentDeleteEnv(t)
			e.h.Hosts = host.NewRegistry(nil, e.h.Agents)
			e.stack(t)
			if rr := e.do(http.MethodDelete, "/stacks/db"+tt.query, e.token, nil); rr.Code != http.StatusAccepted {
				t.Fatalf("delete: %d %s", rr.Code, rr.Body)
			}
			e.connect(t)
			e.h.ResumePendingStackDeletes(context.Background(), "agent-1")
			if strings.Join(e.frames, ",") != strings.Join(tt.want, ",") {
				t.Errorf("agent frames = %v, want %v", e.frames, tt.want)
			}
			if e.mgr.DirExists("db") {
				t.Error("stack kept")
			}
		})
	}
}

// A stack another operation holds stays pending until a later connect.
func TestPendingStackDeleteSkipsBusyStack(t *testing.T) {
	e := newAgentDeleteEnv(t)
	e.h.StackLocks = stacklock.New()
	e.stack(t)
	ctx := context.Background()
	if rr := e.do(http.MethodDelete, "/stacks/db", e.token, nil); rr.Code != http.StatusAccepted {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	release, err := e.h.StackLocks.TryLock(stacklock.HostKey("agent-1", "db"), stacklock.OpDeploy)
	if err != nil {
		t.Fatal(err)
	}
	e.connect(t)
	e.h.ResumePendingStackDeletes(ctx, "agent-1")
	if len(e.deletes) != 0 || !e.mgr.DirExists("db") || e.status(t) != stacks.DeploymentDeletePending {
		t.Fatalf("busy stack was deleted: deletes %v status %q", e.deletes, e.status(t))
	}
	release()
	e.h.ResumePendingStackDeletes(ctx, "agent-1")
	if e.mgr.DirExists("db") {
		t.Fatal("stack not deleted once free")
	}
}
