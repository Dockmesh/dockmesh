package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// newLoginEnv is an authzEnv with the production login limiter, an audit
// log and the given lockout policy (max attempts, 0 = off).
func newLoginEnv(t *testing.T, lockoutMax int) *authzEnv {
	t.Helper()
	e := newAuthzEnv(t)
	store := settings.NewStore(e.db)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := auth.LoadPolicy(store)
	p.LockoutMaxAttempts = lockoutMax
	if err := auth.SavePolicy(context.Background(), store, p); err != nil {
		t.Fatal(err)
	}
	e.h.Auth.SetSettings(store)
	e.h.LoginLimiter = NewLoginLimiter()
	e.h.Audit = audit.NewService(e.db, "")
	return e
}

func loginAs(t *testing.T, e *authzEnv, remote, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, e.h.Login, "/auth/login", remote, map[string]string{"username": username, "password": password})
}

func mfaAs(t *testing.T, e *authzEnv, remote, token, code string) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, e.h.LoginMFA, "/auth/mfa", remote, map[string]string{"mfa_token": token, "code": code})
}

// remoteN is a distinct client address per attempt, so only the account
// limit can trip.
func remoteN(i int) string { return "198.51.100." + strconv.Itoa(i%250+1) + ":1000" }

type blockEntry struct {
	Target  string
	Details map[string]string
}

func blockEntries(t *testing.T, e *authzEnv) []blockEntry {
	t.Helper()
	rows, err := e.db.Query(`SELECT target, details FROM audit_log WHERE action = ? ORDER BY id`, audit.ActionLoginBlocked)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []blockEntry
	for rows.Next() {
		var b blockEntry
		var details string
		if err := rows.Scan(&b.Target, &details); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(details), &b.Details); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func failedAttempts(t *testing.T, e *authzEnv, userID string) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT failed_login_attempts FROM users WHERE id = ?`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoginAccountLimit(t *testing.T) {
	tests := []struct {
		name     string
		lockout  int
		create   bool
		attempt  string // username typed for the failures
		wantKey  string
		finalPwd string
	}{
		{name: "known user, lockout off", create: true, attempt: "alice", wantKey: "alice", finalPwd: testPassword},
		{name: "known user, other case and spaces", lockout: 5, create: true, attempt: "  ALICE ", wantKey: "alice", finalPwd: testPassword},
		{name: "unknown username", attempt: "Nobody", wantKey: "nobody", finalPwd: "whatever"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t, tt.lockout)
			if tt.create {
				e.user("alice", "viewer")
			}
			for i := range auth.AccountLimitMax {
				if rr := loginAs(t, e, remoteN(i), tt.attempt, "wrong"); rr.Code != http.StatusUnauthorized {
					t.Fatalf("attempt %d: %d %s, want 401", i+1, rr.Code, rr.Body)
				}
			}
			rr := loginAs(t, e, remoteN(auth.AccountLimitMax), tt.wantKey, tt.finalPwd)
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("after %d failures: %d %s, want 429", auth.AccountLimitMax, rr.Code, rr.Body)
			}
			retry, _ := strconv.Atoi(rr.Header().Get("Retry-After"))
			if retry < 14*60 || retry > 15*60+1 {
				t.Errorf("Retry-After = %d, want about 15 minutes", retry)
			}
			blocks := blockEntries(t, e)
			if len(blocks) != 1 {
				t.Fatalf("lockout audit entries = %d, want 1: %+v", len(blocks), blocks)
			}
			if d := blocks[0].Details; d["kind"] != "account" || d["key"] != tt.wantKey || d["endpoint"] != "login" || d["until"] == "" {
				t.Errorf("audit details = %v", d)
			}
		})
	}
}

func TestLoginIPLimit(t *testing.T) {
	e := newLoginEnv(t, 0)
	e.user("alice", "viewer")
	const remote = "203.0.113.9:4000"
	// Spread over many names so no account gets near its own limit.
	for i := range loginIPMax - 1 {
		name := "user" + strconv.Itoa(i%20)
		if rr := loginAs(t, e, remote, name, "wrong"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d %s, want 401", i+1, rr.Code, rr.Body)
		}
	}
	if rr := loginAs(t, e, remote, "alice", testPassword); rr.Code != http.StatusOK {
		t.Fatalf("success below the IP limit: %d %s", rr.Code, rr.Body)
	}
	if n := len(blockEntries(t, e)); n != 0 {
		t.Fatalf("lockout audit entries before the IP limit = %d", n)
	}
	// The success must not have reset the IP bucket.
	if rr := loginAs(t, e, remote, "user0", "wrong"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("failure %d: %d, want 401", loginIPMax, rr.Code)
	}
	rr := loginAs(t, e, remote, "alice", testPassword)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures from one IP: %d, want 429 with Retry-After", loginIPMax, rr.Code)
	}
	if rr := loginAs(t, e, "203.0.113.10:4000", "alice", testPassword); rr.Code != http.StatusOK {
		t.Fatalf("other IP: %d, want 200", rr.Code)
	}
	blocks := blockEntries(t, e)
	if len(blocks) != 1 {
		t.Fatalf("lockout audit entries = %d, want 1: %+v", len(blocks), blocks)
	}
	if d := blocks[0].Details; d["kind"] != "ip" || d["key"] != "203.0.113.9" || d["endpoint"] != "login" {
		t.Errorf("audit details = %v", d)
	}
}

func TestLoginMFAFailuresCount(t *testing.T) {
	tests := []struct {
		name      string
		lockout   int
		wrongPwd  int
		wrongCode int
		want      int // status for a correct code afterwards
		wantKind  string
	}{
		{name: "policy lockout", lockout: 5, wrongPwd: 3, wrongCode: 2, want: http.StatusLocked, wantKind: auth.BlockLockout},
		{name: "account limit with lockout off", lockout: 0, wrongPwd: 4, wrongCode: 6, want: http.StatusTooManyRequests, wantKind: auth.BlockRateLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t, tt.lockout)
			u := e.user("dave", "viewer")
			secret := enrollTOTP(t, e, u.ID)

			for i := range tt.wrongPwd {
				if rr := loginAs(t, e, remoteN(i), "dave", "wrong"); rr.Code != http.StatusUnauthorized {
					t.Fatalf("wrong password %d: %d", i+1, rr.Code)
				}
			}
			pending := e.login("dave")
			if !pending.MFARequired {
				t.Fatal("expected an MFA challenge")
			}
			if got := failedAttempts(t, e, u.ID); got != tt.wrongPwd {
				t.Fatalf("after the password step failed_login_attempts = %d, want %d", got, tt.wrongPwd)
			}
			for i := range tt.wrongCode {
				if rr := mfaAs(t, e, remoteN(i), pending.MFAToken, "000000"); rr.Code != http.StatusUnauthorized {
					t.Fatalf("wrong code %d: %d %s", i+1, rr.Code, rr.Body)
				}
			}
			if got := failedAttempts(t, e, u.ID); got != tt.wrongPwd+tt.wrongCode {
				t.Errorf("failed_login_attempts = %d, want %d", got, tt.wrongPwd+tt.wrongCode)
			}
			code, _ := totp.GenerateCode(secret, time.Now())
			if rr := mfaAs(t, e, remoteN(99), pending.MFAToken, code); rr.Code != tt.want {
				t.Fatalf("correct code on a blocked account: %d %s, want %d", rr.Code, rr.Body, tt.want)
			}
			blocks := blockEntries(t, e)
			if len(blocks) != 1 {
				t.Fatalf("lockout audit entries = %d, want 1: %+v", len(blocks), blocks)
			}
			if d := blocks[0].Details; d["kind"] != "account" || d["key"] != "dave" || d["endpoint"] != "mfa" || d["reason"] != tt.wantKind {
				t.Errorf("audit details = %v", d)
			}

			unlock := chiRouterFor(http.MethodPost, "/users/{id}/unlock", e.h.UnlockUser)
			req := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/unlock", nil)
			rr := httptest.NewRecorder()
			unlock.ServeHTTP(rr, req)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("unlock: %d", rr.Code)
			}
			if rr := mfaAs(t, e, remoteN(100), pending.MFAToken, code); rr.Code != http.StatusOK {
				t.Fatalf("correct code after unlock: %d %s", rr.Code, rr.Body)
			}
			var count, until int64
			if err := e.db.QueryRow(`SELECT failed_login_attempts + login_limit_count, login_limit_until FROM users WHERE id = ?`, u.ID).Scan(&count, &until); err != nil {
				t.Fatal(err)
			}
			if count != 0 || until != 0 {
				t.Errorf("counters after a full login = %d / %d, want 0", count, until)
			}
		})
	}
}

func TestUnlockClearsAccountLimit(t *testing.T) {
	e := newLoginEnv(t, 0)
	u := e.user("erin", "viewer")
	for i := range auth.AccountLimitMax {
		loginAs(t, e, remoteN(i), "erin", "wrong")
	}
	if rr := loginAs(t, e, remoteN(50), "erin", testPassword); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked account: %d, want 429", rr.Code)
	}
	unlock := chiRouterFor(http.MethodPost, "/users/{id}/unlock", e.h.UnlockUser)
	rr := httptest.NewRecorder()
	unlock.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/unlock", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unlock: %d", rr.Code)
	}
	if rr := loginAs(t, e, remoteN(51), "erin", testPassword); rr.Code != http.StatusOK {
		t.Fatalf("after unlock: %d %s, want 200", rr.Code, rr.Body)
	}
}
