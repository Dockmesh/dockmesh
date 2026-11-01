package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Backup jobs, runs and saved targets of hosts outside a caller's scope
// stay hidden, and so does the system backup for everyone but an admin.
func TestBackupReadsFollowScope(t *testing.T) {
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
	e.h.BackupTargets = targets.NewTargetStore(e.db)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/jobs", e.h.ListBackupJobs)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/jobs/{id}", e.h.GetBackupJob)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs", e.h.ListBackupRuns)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs/{id}", e.h.GetBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/targets", e.h.ListBackupTargets)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Post("/backups/targets/{id}/test", e.h.TestBackupTarget)
		r.With(middleware.RequirePerm(rbac.PermBackupsDelete)).Delete("/backups/targets/{id}", e.h.DeleteBackupTarget)
		r.With(middleware.RequirePerm(rbac.PermSystemView)).Get("/system/backup-status", e.h.BackupStatus)
	})
	for _, stmt := range []string{
		`INSERT INTO backup_targets (id, name, type) VALUES (1, 'sys-share', 'local'), (2, 'prod-share', 'local'), (3, 'spare', 'local'), (4, 'server-share', 'local')`,
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (7, 'sys', 'local', 1, 'local', '[{"type":"system","name":"system"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (1, 'web', 'local', 4, 'local', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_jobs (id, name, target_type, target_id, host_id, sources) VALUES (2, 'remote', 'local', 2, 'agent-1', '[{"type":"stack","name":"api"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, error, sources_json) VALUES (10, 7, 'sys', 'failed', 'smb://nas.example/backups: access denied', '[{"type":"system","name":"system"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (11, 2, 'remote', 'success', '[{"type":"stack","name":"api"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (12, 1, 'web', 'failed', '[{"type":"stack","name":"web"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, status, sources_json) VALUES (13, 99, 'gone', 'success', '[{"type":"volume","name":"old"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	e.role("prod-backups", []rbac.Perm{rbac.PermBackupsView, rbac.PermSystemView}, rbac.Scope{Type: "host_tag", Value: "prod"})
	e.user("boss", "admin")
	e.user("look", "viewer")
	tagged := e.user("thadmin", "host-admin")
	e.scopeUser(tagged.ID, "prod")
	e.user("team", "prod-backups")

	names := func(user, path, field string) string {
		t.Helper()
		rr := e.do(http.MethodGet, path, e.login(user).AccessToken, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", user, path, rr.Code, rr.Body)
		}
		var got []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range got {
			out = append(out, x[field].(string))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}

	tests := []struct {
		user, jobs, runs, targets string
		lastError                 bool
	}{
		{"boss", "remote,sys,web", "gone,remote,sys,web", "prod-share,server-share,spare,sys-share", true},
		{"look", "remote,web", "remote,web", "prod-share,server-share,spare", false},
		{"thadmin", "remote", "remote", "prod-share,spare", false},
		{"team", "remote", "remote", "prod-share,spare", false},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			if got := names(tt.user, "/backups/jobs", "name"); got != tt.jobs {
				t.Errorf("jobs %s, want %s", got, tt.jobs)
			}
			if got := names(tt.user, "/backups/runs", "job_name"); got != tt.runs {
				t.Errorf("runs %s, want %s", got, tt.runs)
			}
			if got := names(tt.user, "/backups/targets", "name"); got != tt.targets {
				t.Errorf("targets %s, want %s", got, tt.targets)
			}
			rr := e.do(http.MethodGet, "/system/backup-status", e.login(tt.user).AccessToken, nil)
			if has := strings.Contains(rr.Body.String(), "access denied"); has != tt.lastError {
				t.Errorf("system backup error in status: %v, want %v (%d %s)", has, tt.lastError, rr.Code, rr.Body)
			}
		})
	}

	tok := e.login("thadmin").AccessToken
	for _, tt := range []struct {
		name, method, path string
		want               int
	}{
		{"system job", http.MethodGet, "/backups/jobs/7", http.StatusNotFound},
		{"server job", http.MethodGet, "/backups/jobs/1", http.StatusNotFound},
		{"own job", http.MethodGet, "/backups/jobs/2", http.StatusOK},
		{"system run", http.MethodGet, "/backups/runs/10", http.StatusNotFound},
		{"server run", http.MethodGet, "/backups/runs/12", http.StatusNotFound},
		{"run of a deleted job", http.MethodGet, "/backups/runs/13", http.StatusNotFound},
		{"own run", http.MethodGet, "/backups/runs/11", http.StatusOK},
		{"test the system job's target", http.MethodPost, "/backups/targets/1/test", http.StatusForbidden},
		{"delete the server job's target", http.MethodDelete, "/backups/targets/4", http.StatusForbidden},
	} {
		t.Run("thadmin "+tt.name, func(t *testing.T) {
			if rr := e.do(tt.method, tt.path, tok, nil); rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}
