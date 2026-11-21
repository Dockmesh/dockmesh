package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A run whose archive is gone is refused with the reason,
// listed without it, shown with it, and only such a run can be removed
// from the list.
func TestBackupRunArchiveMissingAPI(t *testing.T) {
	e := newBackupRunHostEnv(t)
	dir := t.TempDir()
	targets.SetLocalRoots(func() []string { return []string{dir} })
	t.Cleanup(func() { targets.SetLocalRoots(nil) })
	svc := backup.NewService(e.db, nil, nil, nil, backup.SystemPaths{})
	e.h.Backups, e.h.BackupTargets = svc, svc.Targets()
	e.h.Audit = audit.NewService(e.db, t.TempDir()+"/genesis")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs", e.h.ListBackupRuns)
		r.With(middleware.RequirePerm(rbac.PermBackupsView)).Get("/backups/runs/{id}", e.h.GetBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsDelete)).Delete("/backups/runs/{id}", e.h.DeleteBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/verify", e.h.VerifyBackupRun)
	})
	loc, _ := json.Marshal(map[string]any{"type": "local", "config": map[string]any{"path": dir}})
	for _, stmt := range []string{
		`INSERT INTO backup_targets (id, name, type, config_json) VALUES (1, 'nas', 'local', '{"path":"` + dir + `"}')`,
		`INSERT INTO backup_jobs (id, name, target_id, target_type, host_id, sources) VALUES (1, 'db', 1, 'local', 'local', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sources_json, target_id, target_location)
			VALUES (5, 1, 'db', 'local', 'success', 'jobs/1-db/r5.tar.gz', '[{"type":"volume","name":"data"}]', 1, '` + string(loc) + `')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("boss").AccessToken
	errOf := func(rr interface{ Bytes() []byte }) (body struct{ Error, Code string }) {
		_ = json.Unmarshal(rr.Bytes(), &body)
		return body
	}

	rr := e.do(http.MethodDelete, "/backups/runs/5", tok, nil)
	if rr.Code != http.StatusConflict || errOf(rr.Body).Code != "archive_present" {
		t.Fatalf("remove before the archive was found missing: %d %s", rr.Code, rr.Body)
	}

	rr = e.do(http.MethodPost, "/backups/runs/5/verify", tok, nil)
	if body := errOf(rr.Body); rr.Code != http.StatusConflict || body.Code != "archive_missing" || !strings.Contains(body.Error, "jobs/1-db/r5.tar.gz") {
		t.Fatalf("verify of a run whose archive is gone: %d %s", rr.Code, rr.Body)
	}

	var list []backup.Run
	rr = e.do(http.MethodGet, "/backups/runs", tok, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	if list[0].ArchiveMissingAt == nil || list[0].ArchiveMissingReason != "" {
		t.Fatalf("listed run: missing %v, reason %q", list[0].ArchiveMissingAt, list[0].ArchiveMissingReason)
	}
	var one backup.Run
	rr = e.do(http.MethodGet, "/backups/runs/5", tok, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &one); err != nil || !strings.Contains(one.ArchiveMissingReason, dir) {
		t.Fatalf("run: %d %s", rr.Code, rr.Body)
	}

	rr = e.do(http.MethodDelete, "/backups/runs/5", tok, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body)
	}
	if rr = e.do(http.MethodGet, "/backups/runs/5", tok, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("removed run still there: %d %s", rr.Code, rr.Body)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND target = 'backup-run:5'`, audit.ActionBackupRunRemove).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries for the removal: %d %v", n, err)
	}
}
