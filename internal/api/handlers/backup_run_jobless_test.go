package handlers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A run whose job row is gone and that recorded no target is read from
// the saved target holding its archive; when none does, it answers 409
// run_job_deleted, never 500. Such runs stay admin-only. Restore maps the
// same error the same way; it needs a Docker host, so the backup package
// tests it.
func TestBackupRunWithoutJobRowAPI(t *testing.T) {
	e := newBackupRunHostEnv(t)
	dir := t.TempDir()
	targets.SetLocalRoots(func() []string { return []string{dir} })
	t.Cleanup(func() { targets.SetLocalRoots(nil) })
	svc := backup.NewService(e.db, nil, nil, nil, backup.SystemPaths{})
	e.h.Backups, e.h.BackupTargets = svc, svc.Targets()
	e.h.Audit = audit.NewService(e.db, t.TempDir()+"/genesis")
	e.h.Operations = operations.New(e.db)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/verify", e.h.VerifyBackupRun)
		r.With(middleware.RequirePerm(rbac.PermBackupsRestore)).Post("/backups/runs/{id}/restore", e.h.RestoreBackup)
		r.With(middleware.RequirePerm(rbac.PermBackupsDownload)).Get("/backups/runs/{id}/archive", e.h.DownloadBackupArchive)
	})

	archive := volumeArchive(t)
	sum := sha256.Sum256(archive)
	const rel = "jobs/db/20250101-000000.tar.gz"
	if err := os.MkdirAll(filepath.Join(dir, "jobs", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO backup_targets (id, name, type, config_json) VALUES (1, 'nas', 'local', '{"path":"` + dir + `"}')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sha256, size_bytes, sources_json)
			VALUES (5, 9, 'db', 'local', 'success', '` + rel + `', '` + hex.EncodeToString(sum[:]) + `', ` + strconv.Itoa(len(archive)) + `, '[{"type":"volume","name":"data"}]')`,
		`INSERT INTO backup_runs (id, job_id, job_name, host_id, status, target_path, sha256, size_bytes, sources_json)
			VALUES (6, 9, 'db', 'local', 'success', 'jobs/db/gone.tar.gz', '` + hex.EncodeToString(sum[:]) + `', 1, '[{"type":"volume","name":"data"}]')`,
	} {
		if _, err := e.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	boss := e.login("boss").AccessToken
	scoped := e.login("thadmin").AccessToken

	for _, tt := range []struct {
		name     string
		method   string
		path     string
		tok      string
		want     int
		wantCode string
	}{
		{"verify, archive on a saved target", http.MethodPost, "/backups/runs/5/verify", boss, http.StatusOK, ""},
		{"download, archive on a saved target", http.MethodGet, "/backups/runs/5/archive", boss, http.StatusOK, ""},
		{"verify, archive on no saved target", http.MethodPost, "/backups/runs/6/verify", boss, http.StatusConflict, "run_job_deleted"},
		{"download, archive on no saved target", http.MethodGet, "/backups/runs/6/archive", boss, http.StatusConflict, "run_job_deleted"},
		{"verify by a scoped user", http.MethodPost, "/backups/runs/5/verify", scoped, http.StatusForbidden, ""},
		{"restore by a scoped user", http.MethodPost, "/backups/runs/5/restore", scoped, http.StatusForbidden, ""},
		{"download by a scoped user", http.MethodGet, "/backups/runs/5/archive", scoped, http.StatusForbidden, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(tt.method, tt.path, tt.tok, nil)
			if rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
			if tt.wantCode == "" {
				return
			}
			var body struct{ Error, Code string }
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Code != tt.wantCode || body.Error == "" {
				t.Fatalf("body %s, want code %s", rr.Body, tt.wantCode)
			}
		})
	}
}

func volumeArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
