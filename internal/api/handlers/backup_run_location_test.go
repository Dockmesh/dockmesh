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

// A target edit answers how many archives stay at the old place, and a
// run whose archive is gone answers 409 archive_missing naming where it
// was written.
func TestBackupRunLocationAPI(t *testing.T) {
	e := newBackupRunHostEnv(t)
	oldDir, newDir := t.TempDir(), t.TempDir()
	targets.SetLocalRoots(func() []string { return []string{oldDir, newDir} })
	t.Cleanup(func() { targets.SetLocalRoots(nil) })
	svc := backup.NewService(e.db, nil, nil, nil, backup.SystemPaths{})
	e.h.Backups, e.h.BackupTargets = svc, svc.Targets()
	e.h.Audit = audit.NewService(e.db, t.TempDir()+"/genesis")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsUpdate)).Put("/backups/targets/{id}", e.h.UpdateBackupTarget)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/verify", e.h.VerifyBackupRun)
	})
	loc, _ := json.Marshal(map[string]any{"type": "local", "config": map[string]any{"path": oldDir}})
	for _, stmt := range []string{
		`INSERT INTO backup_targets (id, name, type, config_json) VALUES (1, 'nas', 'local', '{"path":"` + oldDir + `"}')`,
		`INSERT INTO backup_jobs (id, name, target_id, target_type, host_id, sources) VALUES (1, 'db', 1, 'local', 'local', '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sources_json, target_id, target_location)
			VALUES (5, 1, 'db', 'local', 'success', 'jobs/1-db/r5.tar.gz', '[{"type":"volume","name":"data"}]', 1, '` + string(loc) + `')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("boss").AccessToken

	rr := e.do(http.MethodPut, "/backups/targets/1", tok, map[string]any{"name": "nas", "type": "local", "config": map[string]any{"path": newDir}})
	if rr.Code != http.StatusOK {
		t.Fatalf("update target: %d %s", rr.Code, rr.Body)
	}
	var got struct {
		RunsAtPreviousLocation int `json:"runs_at_previous_location"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.RunsAtPreviousLocation != 1 {
		t.Fatalf("runs_at_previous_location in %s", rr.Body)
	}
	var details string
	if err := e.db.QueryRow(`SELECT details FROM audit_log WHERE action = ? AND target = 'backup-target:1'`, audit.ActionBackupTargetUpdate).Scan(&details); err != nil {
		t.Fatalf("no audit entry for the target edit: %v", err)
	}
	if !strings.Contains(details, oldDir) || !strings.Contains(details, newDir) {
		t.Errorf("audit details %s don't name both locations", details)
	}

	rr = e.do(http.MethodPost, "/backups/runs/5/verify", tok, nil)
	var body struct{ Error, Code string }
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != http.StatusConflict || body.Code != "archive_missing" || !strings.Contains(body.Error, oldDir) {
		t.Fatalf("verify of a run whose archive is gone: %d %s", rr.Code, rr.Body)
	}
}
