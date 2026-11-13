package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/setup"
)

const setupCommitBody = `{"data_dir":"/tmp/dm","service_user":{"mode":"existing","username":"dockmesh"},
	"admin":{"username":"admin","password":"long-enough-pw"},"public_url":"https://dm.example.com"}`

func postSetup(fn http.HandlerFunc, path, body string) int {
	return postSetupAs(fn, path, body, "")
}

func postSetupAs(fn http.HandlerFunc, path, body, token string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.AddCookie(&http.Cookie{Name: middleware.SetupCookie, Value: token})
	}
	fn(rec, req)
	return rec.Code
}

// setupCodeFile is a code file for one test.
func setupCodeFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "setup-code.hash")
}

// openSetupSession writes a fresh code to path, the way new-code does, and
// enters it; it returns the session token.
func openSetupSession(t *testing.T, st *setup.State, path string) string {
	t.Helper()
	code, err := setup.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := setup.NewCodeRecord(code, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	token, err := st.Codes().Redeem("192.0.2.1", code)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestSetupCommitIsExclusive(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	codeFile := setupCodeFile(t)
	st := setup.NewForced(codeFile)
	token := openSetupSession(t, st, codeFile)
	h := &Handlers{
		SetupState: st,
		SetupCommit_: func(context.Context, setup.CommitInput, func(string, string, string)) error {
			<-release
			return nil
		},
	}
	if code := postSetup(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody); code != http.StatusUnauthorized {
		t.Fatalf("commit without a setup session: got %d, want 401", code)
	}
	if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, token); code != http.StatusAccepted {
		t.Fatalf("first commit: %d", code)
	}
	if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, token); code != http.StatusUnauthorized {
		t.Fatalf("second commit from the installing session: got %d, want 401", code)
	}
	other := openSetupSession(t, st, codeFile)
	if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, other); code != http.StatusConflict {
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
	if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, other); code != http.StatusGone {
		t.Fatalf("commit after install: got %d, want 410", code)
	}
}

func TestSetupProbesClosedAfterSetup(t *testing.T) {
	st := setup.NewForced(setupCodeFile(t))
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

// The wizard page may have been open since boot; any account created in
// the meantime closes it, admin or not.
func TestSetupCommitRefusedOnceAccountExists(t *testing.T) {
	tests := []struct {
		name     string
		account  string // SQL run after boot, before the commit
		wantCode int
		wantRun  bool
	}{
		{name: "still no account", wantCode: http.StatusAccepted, wantRun: true},
		{name: "admin created after boot", account: accountAdmin, wantCode: http.StatusConflict},
		{name: "viewer created after boot", account: accountViewer, wantCode: http.StatusConflict},
		{name: "account without role created after boot", account: accountBare, wantCode: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			codeFile := setupCodeFile(t)
			st, _, _, err := setup.NewForBoot(context.Background(), database, setup.Facts{}, codeFile)
			if err != nil || !st.Active() {
				t.Fatalf("setup not active on an empty database: %v", err)
			}
			token := openSetupSession(t, st, codeFile)
			if tt.account != "" {
				if _, err := database.Exec(tt.account); err != nil {
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
			if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, token); code != tt.wantCode {
				t.Fatalf("commit: got %d, want %d", code, tt.wantCode)
			}
			select {
			case <-ran:
				if !tt.wantRun {
					t.Fatal("install ran although an account exists")
				}
			case <-time.After(time.Second):
				if tt.wantRun {
					t.Fatal("install did not run")
				}
			}
			if tt.account != "" && st.Active() {
				t.Fatal("setup still active after an account was found")
			}
		})
	}
}

const (
	accountBare   = `INSERT INTO users (id, username, password) VALUES ('u1', 'cli-user', 'x')`
	accountViewer = accountBare + `; INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'viewer', 'global')`
	accountAdmin  = accountBare + `; INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'admin', 'global')`
)

// A runner that finds an account created after the handler's check ends
// setup mode, so the gate stops answering 503 to the other routes.
func TestSetupCommitLosingTheRaceEndsSetup(t *testing.T) {
	codeFile := setupCodeFile(t)
	st := setup.NewForced(codeFile)
	token := openSetupSession(t, st, codeFile)
	h := &Handlers{
		SetupState: st,
		SetupCommit_: func(context.Context, setup.CommitInput, func(string, string, string)) error {
			return auth.ErrAlreadySetUp
		},
	}
	if code := postSetupAs(h.SetupCommit, "/api/v1/setup/commit", setupCommitBody, token); code != http.StatusAccepted {
		t.Fatalf("commit: %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.SetupState.Active() {
		if time.Now().After(deadline) {
			t.Fatal("setup still active after the install found an existing account")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Every setup endpoint but status and the code form refuses a request
// without a setup session, even if SetupGate were bypassed.
func TestSetupHandlersNeedSession(t *testing.T) {
	st := setup.NewForced(setupCodeFile(t))
	h := &Handlers{SetupState: st, SetupCommit_: func(context.Context, setup.CommitInput, func(string, string, string)) error { return nil }}
	tests := []struct {
		name string
		fn   http.HandlerFunc
		body string
	}{
		{"preflight", h.SetupPreflight, ""},
		{"server info", h.SetupServerInfo, ""},
		{"data dir", h.SetupValidateDataDir, `{"path":"/etc"}`},
		{"user", h.SetupValidateUser, `{"mode":"existing","username":"root"}`},
		{"test url", h.SetupTestURL, `{"url":"http://192.0.2.1"}`},
		{"commit", h.SetupCommit, setupCommitBody},
		{"restore", h.SetupRestore, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, token := range []string{"", "forged"} {
				if code := postSetupAs(tt.fn, "/api/v1/setup/x", tt.body, token); code != http.StatusUnauthorized {
					t.Fatalf("token %q: got %d, want 401", token, code)
				}
			}
		})
	}
}

func TestSetupCodeEndpoint(t *testing.T) {
	codeFile := setupCodeFile(t)
	st := setup.NewForced(codeFile)
	h := &Handlers{SetupState: st}
	if code := postSetup(h.SetupCode, "/api/v1/setup/code", `{"code":"AAAA-AAAA-AAAA"}`); code != http.StatusConflict {
		t.Fatalf("no code file: got %d, want 409", code)
	}
	openSetupSession(t, st, codeFile)
	rec := httptest.NewRecorder()
	h.SetupCode(rec, httptest.NewRequest(http.MethodPost, "/api/v1/setup/code", strings.NewReader(`{"code":"AAAA-AAAA-AAAA"}`)))
	var body struct {
		AttemptsLeft int `json:"attempts_left"`
	}
	if rec.Code != http.StatusUnauthorized || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.AttemptsLeft != 4 {
		t.Fatalf("wrong code: %d %s", rec.Code, rec.Body)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("a wrong code set a cookie")
	}
}
