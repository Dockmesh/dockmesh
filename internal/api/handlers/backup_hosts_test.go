package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func newBackupHostEnv(t *testing.T) *authzEnv {
	t.Helper()
	e := newScopeEnv(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'online'), ('agent-2', 'agent-2', 'offline')`,
		`INSERT INTO backup_targets (id, name, type) VALUES (1, 'share', 'local')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-1", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "local", []string{"srv"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Agents = agents.NewService(e.db, nil, "http://dockmesh.test", "wss://dockmesh.test/connect")
	e.h.Hosts = host.NewRegistry(nil, e.h.Agents)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "idle", "pay"} {
		if _, err := mgr.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	e.h.Stacks = mgr
	for stack, hostID := range map[string]string{"web": "agent-1", "pay": "agent-2"} {
		if err := e.h.Deployments.Set(ctx, stack, hostID, "deployed"); err != nil {
			t.Fatal(err)
		}
	}
	volumes := map[string][]string{"local": {"data"}, "agent-1": {"pgdata"}, "agent-2": {"cache"}}
	e.h.backupVolumeCheck = func(_ context.Context, hostID, name string) error {
		if strings.ContainsAny(name, "/.") {
			t.Errorf("volume lookup reached with %q", name)
		}
		if slices.Contains(volumes[hostID], name) {
			return nil
		}
		return fmt.Errorf("volume %s not found on host %s", name, hostID)
	}
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/hosts", e.h.ListBackupHosts)
		r.With(middleware.RequirePerm(rbac.PermBackupsCreate)).Post("/backups/jobs", e.h.CreateBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Put("/backups/jobs/{id}", e.h.UpdateBackupJob)
	})
	return e
}

func TestBackupHostsFollowScope(t *testing.T) {
	e := newBackupHostEnv(t)
	e.user("boss", "admin")
	tagged := e.user("prodadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")
	e.user("look", "viewer")

	for _, tt := range []struct{ user, want string }{
		{"boss", "agent-1,agent-2,local"},
		{"prodadmin", "agent-1"},
		{"look", ""},
	} {
		t.Run(tt.user, func(t *testing.T) {
			rr := e.do(http.MethodGet, "/backups/hosts", e.login(tt.user).AccessToken, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			var got []BackupHost
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, h := range got {
				if !h.CanCreate || !h.CanUpdate {
					t.Errorf("%s: create %v update %v", h.ID, h.CanCreate, h.CanUpdate)
				}
				ids = append(ids, h.ID)
			}
			slices.Sort(ids)
			if s := strings.Join(ids, ","); s != tt.want {
				t.Errorf("hosts %q, want %q", s, tt.want)
			}
		})
	}
}

func TestBackupJobPlacement(t *testing.T) {
	e := newBackupHostEnv(t)
	e.user("boss", "admin")
	tagged := e.user("prodadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")
	boss := e.login("boss").AccessToken
	prod := e.login("prodadmin").AccessToken

	tests := []struct {
		name, bearer string
		body         map[string]any
		want         int
		wantHost     string
	}{
		{"server volume", boss, backupJobBody("v1", "", "volume", "data"), http.StatusCreated, "local"},
		{"agent volume", boss, backupJobBody("v2", "agent-1", "volume", "pgdata"), http.StatusCreated, "agent-1"},
		{"volume of another host", boss, backupJobBody("v3", "agent-1", "volume", "data"), http.StatusBadRequest, ""},
		{"unknown host", boss, backupJobBody("v4", "nope", "volume", "data"), http.StatusBadRequest, ""},
		{"stack takes its deployment host", boss, backupJobBody("s1", "", "stack", "web"), http.StatusCreated, "agent-1"},
		{"stack on the wrong host", boss, backupJobBody("s2", "local", "stack", "web"), http.StatusBadRequest, ""},
		{"undeployed stack", boss, backupJobBody("s3", "", "stack", "idle"), http.StatusBadRequest, ""},
		{"missing stack", boss, backupJobBody("s4", "", "stack", "ghost"), http.StatusBadRequest, ""},
		{"path as volume name", boss, backupJobBody("v5", "local", "volume", "../images/x/get"), http.StatusBadRequest, ""},
		{"system job on an agent", boss, backupJobBody("sys2", "agent-1", "system", "*"), http.StatusBadRequest, ""},
		{"scoped volume in scope", prod, backupJobBody("p1", "agent-1", "volume", "pgdata"), http.StatusCreated, "agent-1"},
		{"scoped volume out of scope", prod, backupJobBody("p2", "agent-2", "volume", "cache"), http.StatusForbidden, ""},
		{"scoped missing volume out of scope", prod, backupJobBody("p3", "agent-2", "volume", "nope"), http.StatusForbidden, ""},
		{"scoped stack job on an out-of-scope host", prod, backupJobBody("p4", "", "stack", "idle"), http.StatusForbidden, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(http.MethodPost, "/backups/jobs", tt.bearer, tt.body)
			if rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			if tt.wantHost != "" {
				assertJobHost(t, rr, tt.wantHost)
			}
		})
	}
}

// Jobs keep saving while their host and source stay the same, including
// jobs stored without a host and jobs whose volume is gone.
func TestBackupJobUpdateKeepsPlacement(t *testing.T) {
	e := newBackupHostEnv(t)
	e.user("boss", "admin")
	boss := e.login("boss").AccessToken
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (1, 'legacy', 'local', 1, '', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (2, 'old-vol', 'local', 1, 'agent-1', '[{"type":"volume","name":"removed"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (3, 'moving', 'local', 1, 'local', '[{"type":"volume","name":"data"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, path string
		body       map[string]any
		want       int
		wantHost   string
	}{
		{"legacy job keeps the server host", "/backups/jobs/1", backupJobBody("legacy", "", "stack", "web"), http.StatusOK, "local"},
		{"legacy job moved to the stack's host", "/backups/jobs/1", backupJobBody("legacy", "agent-1", "stack", "web"), http.StatusOK, "agent-1"},
		{"gone volume, same placement", "/backups/jobs/2", backupJobBody("old-vol", "agent-1", "volume", "removed"), http.StatusOK, "agent-1"},
		{"new host without the volume", "/backups/jobs/3", backupJobBody("moving", "agent-1", "volume", "data"), http.StatusBadRequest, ""},
		{"new host and its volume", "/backups/jobs/3", backupJobBody("moving", "agent-1", "volume", "pgdata"), http.StatusOK, "agent-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(http.MethodPut, tt.path, boss, tt.body)
			if rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			if tt.wantHost != "" {
				assertJobHost(t, rr, tt.wantHost)
			}
		})
	}
}

func backupJobBody(name, hostID, kind, src string) map[string]any {
	return map[string]any{"name": name, "host_id": hostID, "target_id": 1, "schedule": "0 3 * * *",
		"retention_count": 3, "enabled": true, "sources": []map[string]string{{"type": kind, "name": src}}}
}

func assertJobHost(t *testing.T, rr *httptest.ResponseRecorder, want string) {
	t.Helper()
	var got struct {
		HostID string `json:"host_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if jobHostID(got.HostID) != want {
		t.Errorf("host %q, want %q", got.HostID, want)
	}
}

// A caller who can't manage backups where a stack runs gets the same answer
// for it as for a stack that doesn't exist.
func TestBackupJobStackOutOfScope(t *testing.T) {
	e := newBackupHostEnv(t)
	srv := e.user("srvadmin", "host-admin")
	e.scopeUser(srv.ID, "srv")
	tok := e.login("srvadmin").AccessToken
	answer := func(hostID, stack string) string {
		t.Helper()
		rr := e.do(http.MethodPost, "/backups/jobs", tok, backupJobBody("j-"+stack, hostID, "stack", stack))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s on %q: %d %s", stack, hostID, rr.Code, rr.Body)
		}
		return strings.ReplaceAll(rr.Body.String(), stack, "S")
	}
	missing := answer("local", "ghost")
	for _, tt := range []struct{ hostID, stack string }{{"local", "pay"}, {"", "pay"}, {"", "ghost"}, {"local", "idle"}} {
		got := answer(tt.hostID, tt.stack)
		if got != missing {
			t.Errorf("%s on %q: %s, want %s", tt.stack, tt.hostID, got, missing)
		}
		if strings.Contains(got, "agent-2") {
			t.Errorf("answer names the deployment host: %s", got)
		}
	}
}
