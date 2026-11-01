package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/setup"
)

const setupCommitBody = `{"data_dir":"/tmp/dm","service_user":{"mode":"existing","username":"dockmesh"},
	"admin":{"username":"admin","password":"longenough"},"public_url":"https://dm.example.com"}`

func postSetup(fn http.HandlerFunc, path, body string) int {
	rec := httptest.NewRecorder()
	fn(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec.Code
}

func TestSetupCommitIsExclusive(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	h := &Handlers{
		SetupState: setup.NewForced(),
		SetupCommit_: func(context.Context, setup.CommitInput, func(string, string, string)) error {
			<-release
			return nil
		},
	}
	if code := postSetup(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody); code != http.StatusAccepted {
		t.Fatalf("first commit: %d", code)
	}
	if code := postSetup(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody); code != http.StatusConflict {
		t.Fatalf("concurrent commit: got %d, want 409", code)
	}
	close(release)
	go func() {
		for h.SetupState.Active() {
			time.Sleep(5 * time.Millisecond)
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("setup mode still active after a successful install")
	}
	if code := postSetup(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody); code != http.StatusGone {
		t.Fatalf("commit after install: got %d, want 410", code)
	}
}

func TestSetupProbesClosedAfterSetup(t *testing.T) {
	st := setup.NewForced()
	st.EndCommit(true)
	h := &Handlers{SetupState: st}
	tests := []struct {
		name string
		fn   http.HandlerFunc
		body string
	}{
		{"data dir", h.SetupValidateDataDir, `{"path":"/etc"}`},
		{"user", h.SetupValidateUser, `{"mode":"existing","username":"root"}`},
		{"test url", h.SetupTestURL, `{"url":"http://169.254.169.254"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := postSetup(tt.fn, "/api/v1/setup/x", tt.body); code != http.StatusGone {
				t.Fatalf("got %d, want 410", code)
			}
		})
	}
}

func TestSetupCommitRefusedOnceAdminExists(t *testing.T) {
	tests := []struct {
		name     string
		admin    bool
		wantCode int
		wantRun  bool
	}{
		{name: "no admin", admin: false, wantCode: http.StatusAccepted, wantRun: true},
		{name: "admin created after boot", admin: true, wantCode: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			st, err := setup.NewFromDB(context.Background(), database)
			if err != nil || !st.Active() {
				t.Fatalf("setup not active on an empty database: %v", err)
			}
			if tt.admin {
				if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'cli-admin', 'x'); INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'admin', 'global')`); err != nil {
					t.Fatal(err)
				}
			}
			ran := make(chan struct{}, 1)
			h := &Handlers{
				DB:         database,
				SetupState: st,
				SetupCommit_: func(context.Context, setup.CommitInput, func(string, string, string)) error {
					ran <- struct{}{}
					return nil
				},
			}
			if code := postSetup(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody); code != tt.wantCode {
				t.Fatalf("commit: got %d, want %d", code, tt.wantCode)
			}
			select {
			case <-ran:
				if !tt.wantRun {
					t.Fatal("install ran although an admin exists")
				}
			case <-time.After(time.Second):
				if tt.wantRun {
					t.Fatal("install did not run")
				}
			}
			if tt.admin && st.Active() {
				t.Fatal("setup still active after an admin was found")
			}
		})
	}
}
