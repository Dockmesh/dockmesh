package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type mapSettings map[string]string

func (m mapSettings) Get(key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

// newBackupRunHostEnv has a server and an agent host tagged prod, and a
// host admin scoped to prod.
func newBackupRunHostEnv(t *testing.T) *authzEnv {
	t.Helper()
	e := newScopeEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'offline')`); err != nil {
		t.Fatal(err)
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-1", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.BackupTargets = targets.NewTargetStore(e.db, nil)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs/{id}", e.h.GetBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsDelete)).Delete("/backups/targets/{id}", e.h.DeleteBackupTarget)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/targets", e.h.ListBackupTargets)
		r.With(middleware.RequirePerm(rbac.PermBackupsCreate)).Post("/backups/jobs", e.h.CreateBackupJob)
	})
	e.user("boss", "admin")
	tagged := e.user("thadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")
	return e
}

// A run is read and restored under the host it was taken on, not the
// host its job points at now.
func TestBackupRunAccessUsesRunHost(t *testing.T) {
	e := newBackupRunHostEnv(t)
	for _, stmt := range []string{
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (1, 'moved-to-server', 'local', 'local', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, host_id, sources) VALUES (2, 'moved-to-agent', 'local', 'agent-1', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, sources_json) VALUES (10, 1, 'moved-to-server', 'agent-1', 'success', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, sources_json) VALUES (11, 2, 'moved-to-agent', 'local', 'success', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (12, 2, 'moved-to-agent', 'success', '[{"type":"volume","name":"data"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("thadmin").AccessToken
	for _, tt := range []struct {
		name string
		path string
		want int
	}{
		{"taken on the agent, job now on the server", "/backups/runs/10", http.StatusOK},
		{"taken on the server, job now on the agent", "/backups/runs/11", http.StatusNotFound},
		{"no host stored falls back to the job", "/backups/runs/12", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if rr := e.do(http.MethodGet, tt.path, tok, nil); rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}

// The target the audit log is archived to is admin-only even when no
// backup job uses it.
func TestAuditArchiveTargetNeedsAdmin(t *testing.T) {
	e := newBackupRunHostEnv(t)
	if _, err := e.db.Exec(`INSERT INTO backup_targets (id, name, type) VALUES (1, 'audit-share', 'local'), (2, 'spare', 'local')`); err != nil {
		t.Fatal(err)
	}
	e.h.AuditRetention = audit.NewRetention(e.db, nil, mapSettings{
		audit.RetentionModeKey:   "archive_target",
		audit.RetentionTargetKey: "1",
	}, nil, t.TempDir())
	tok := e.login("thadmin").AccessToken
	if rr := e.do(http.MethodGet, "/backups/targets", tok, nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "audit-share") {
		t.Errorf("host admin lists the audit target: %d %s", rr.Code, rr.Body)
	}
	job := map[string]any{"name": "onto-audit", "host_id": "agent-1", "target_id": 1, "target_type": "local",
		"sources": []map[string]string{{"type": "volume", "name": "data"}}}
	if rr := e.do(http.MethodPost, "/backups/jobs", tok, job); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "audit") {
		t.Errorf("host admin creates a job on the audit target: %d %s, want 403", rr.Code, rr.Body)
	}
	rr := e.do(http.MethodDelete, "/backups/targets/1", tok, nil)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "audit") {
		t.Errorf("delete audit target as host admin: %d %s, want 403", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodDelete, "/backups/targets/2", tok, nil); rr.Code != http.StatusNoContent {
		t.Errorf("delete unused target as host admin: %d %s, want 204", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodDelete, "/backups/targets/1", e.login("boss").AccessToken, nil); rr.Code == http.StatusForbidden {
		t.Errorf("delete audit target as admin: %d %s", rr.Code, rr.Body)
	}
}

func TestOwnContainerMounts(t *testing.T) {
	cs := []container.Summary{
		{ID: "u1", Image: "postgres:17", Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "app-db"}}},
		{ID: "s1", Image: "ghcr.io/dockmesh/dockmesh:0.5.0", Mounts: []container.MountPoint{
			{Type: mount.TypeVolume, Name: "dockmesh_data"},
			{Type: mount.TypeBind, Name: "", Source: "/var/run/docker.sock"},
		}},
		{ID: "p1", Image: "caddy:2", Labels: map[string]string{"dockmesh.component": "proxy"}, Mounts: []container.MountPoint{
			{Type: mount.TypeVolume, Name: "custom-caddy"},
		}},
	}
	for _, tt := range []struct {
		volume string
		want   bool
	}{
		{"dockmesh_data", true},
		{"custom-caddy", true},
		{"app-db", false},
		{"unused", false},
	} {
		if got := ownContainerMounts("local", cs, tt.volume); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.volume, got, tt.want)
		}
	}
	if err := (&Handlers{}).checkRestoreVolume(context.Background(), "local", "dockmesh-caddy-data"); !errors.Is(err, errOwnVolume) {
		t.Errorf("known own volume: %v, want errOwnVolume", err)
	}
}
