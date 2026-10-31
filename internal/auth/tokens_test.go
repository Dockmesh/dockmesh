package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"

	"github.com/dockmesh/dockmesh/internal/db"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return NewService(database, testSecret), database
}

func mustUser(ctx context.Context, t *testing.T, s *Service, name, role string) *User {
	t.Helper()
	u, err := s.CreateUser(ctx, name, "", "correct horse battery", role)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustLogin(ctx context.Context, t *testing.T, s *Service, name string) *LoginResult {
	t.Helper()
	res, err := s.Login(ctx, name, "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Every kind has its own key and audience: a token signed for one kind
// must not parse as any other.
func TestTokenKindsDontCross(t *testing.T) {
	keys, err := NewTokenKeys(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, signed := range tokenKinds {
		tok, err := keys.Sign(signed, time.Minute, &Claims{UserID: "u"})
		if err != nil {
			t.Fatal(err)
		}
		for _, parsed := range tokenKinds {
			err := keys.Parse(parsed, tok, &Claims{})
			if signed == parsed && err != nil {
				t.Errorf("%s: own kind rejected: %v", signed, err)
			}
			if signed != parsed && err == nil {
				t.Errorf("%s token accepted as %s", signed, parsed)
			}
		}
	}
}

func TestParseRejectsMalformedTokens(t *testing.T) {
	keys, err := NewTokenKeys(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	accessKey := keys.keys[KindAccess]
	sign := func(c jwt.Claims, method jwt.SigningMethod, key any) string {
		s, err := jwt.NewWithClaims(method, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	exp := jwt.NewNumericDate(time.Now().Add(time.Minute))
	tests := []struct {
		name  string
		token string
	}{
		{"pre-upgrade token signed with the raw secret", sign(jwt.MapClaims{
			"uid": "u", "iss": "dockmesh", "exp": exp.Unix(),
		}, jwt.SigningMethodHS256, testSecret)},
		{"right key, no typ", sign(jwt.MapClaims{
			"uid": "u", "iss": "dockmesh", "aud": "dockmesh:access", "exp": exp.Unix(),
		}, jwt.SigningMethodHS256, accessKey)},
		{"right key, wrong typ", sign(jwt.MapClaims{
			"typ": "refresh", "iss": "dockmesh", "aud": "dockmesh:access", "exp": exp.Unix(),
		}, jwt.SigningMethodHS256, accessKey)},
		{"right key, no exp", sign(jwt.MapClaims{
			"typ": "access", "iss": "dockmesh", "aud": "dockmesh:access",
		}, jwt.SigningMethodHS256, accessKey)},
		{"right key, wrong aud", sign(jwt.MapClaims{
			"typ": "access", "iss": "dockmesh", "aud": "dockmesh:refresh", "exp": exp.Unix(),
		}, jwt.SigningMethodHS256, accessKey)},
		{"alg none", sign(jwt.MapClaims{
			"typ": "access", "iss": "dockmesh", "aud": "dockmesh:access", "exp": exp.Unix(),
		}, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType)},
		{"HS512", sign(jwt.MapClaims{
			"typ": "access", "iss": "dockmesh", "aud": "dockmesh:access", "exp": exp.Unix(),
		}, jwt.SigningMethodHS512, accessKey)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := keys.Parse(KindAccess, tt.token, &Claims{}); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestMFAPendingTokenIsNotAnAccessToken(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	mustUser(ctx, t, s, "alice", "admin")
	mfa, err := s.issueMFAPending("whatever")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, mfa); err == nil {
		t.Fatal("mfa-pending token accepted as access token")
	}
	res := mustLogin(ctx, t, s, "alice")
	for name, tok := range map[string]string{"access": res.AccessToken, "refresh": res.RefreshToken} {
		if _, err := s.parseMFAPending(tok); err == nil {
			t.Errorf("%s token accepted as mfa-pending", name)
		}
	}
	if _, err := s.Validate(ctx, res.RefreshToken); err == nil {
		t.Error("refresh token accepted as access token")
	}
	if _, err := s.Refresh(ctx, res.AccessToken); err == nil {
		t.Error("access token accepted as refresh token")
	}
}

func TestValidateFollowsSessionAndUser(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	u := mustUser(ctx, t, s, "bob", "operator")

	tests := []struct {
		name   string
		mutate func(t *testing.T, sid string)
		ok     bool
		role   string
	}{
		{"live session", func(*testing.T, string) {}, true, "operator"},
		{"role changed", func(t *testing.T, _ string) {
			if _, err := s.UpdateUser(ctx, u.ID, "", "viewer", ""); err != nil {
				t.Fatal(err)
			}
		}, true, "viewer"},
		{"session revoked", func(t *testing.T, sid string) {
			if _, err := database.Exec(`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE family_id = ?`, sid); err != nil {
				t.Fatal(err)
			}
		}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mustLogin(ctx, t, s, "bob")
			c := &Claims{}
			if err := s.keys.Parse(KindAccess, res.AccessToken, c); err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, c.SessionID)
			got, err := s.Validate(ctx, res.AccessToken)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && got.Role != tt.role {
				t.Errorf("role = %q, want %q", got.Role, tt.role)
			}
		})
	}

	res := mustLogin(ctx, t, s, "bob")
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, res.AccessToken); err == nil {
		t.Error("access token of a deleted user still valid")
	}
}

func TestRefreshFlagsPreUpgradeTokens(t *testing.T) {
	s, _ := newTestService(t)
	old, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"fam": "x", "seq": 0, "iss": "dockmesh", "sub": "refresh",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(context.Background(), old); !errors.Is(err, ErrSessionOutdated) {
		t.Errorf("pre-upgrade refresh: err = %v, want ErrSessionOutdated", err)
	}
	if _, err := s.Refresh(context.Background(), "garbage"); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("garbage refresh: err = %v, want ErrInvalidToken", err)
	}
}

func TestRevokeUserSessions(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	u := mustUser(ctx, t, s, "carol", "viewer")
	a, b, c := mustLogin(ctx, t, s, "carol"), mustLogin(ctx, t, s, "carol"), mustLogin(ctx, t, s, "carol")
	ca := &Claims{}
	if err := s.keys.Parse(KindAccess, a.AccessToken, ca); err != nil {
		t.Fatal(err)
	}

	n, err := s.RevokeUserSessions(ctx, u.ID, ca.SessionID)
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, err %v; want 2", n, err)
	}
	if _, err := s.Validate(ctx, a.AccessToken); err != nil {
		t.Errorf("kept session rejected: %v", err)
	}
	for _, r := range []*LoginResult{b, c} {
		if _, err := s.Validate(ctx, r.AccessToken); err == nil {
			t.Error("other session's access token still valid")
		}
		if _, err := s.Refresh(ctx, r.RefreshToken); err == nil {
			t.Error("other session's refresh token still valid")
		}
	}

	if _, err := s.RevokeUserSessions(ctx, u.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, a.AccessToken); err == nil {
		t.Error("revoke-all kept a session")
	}
}

func TestWSTicketSingleUse(t *testing.T) {
	s, _ := newTestService(t)
	tok, err := s.IssueWSTicket(WSTicket{UserID: "u", SessionID: "sid", Perm: "containers.exec"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ConsumeWSTicket(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u" || got.SessionID != "sid" || got.Perm != "containers.exec" {
		t.Errorf("identity lost: %+v", got)
	}
	if _, err := s.ConsumeWSTicket(tok); !errors.Is(err, ErrTicketUsed) {
		t.Errorf("replay: err = %v, want ErrTicketUsed", err)
	}

	ctx := context.Background()
	mustUser(ctx, t, s, "dave", "admin")
	access := mustLogin(ctx, t, s, "dave").AccessToken
	if _, err := s.ConsumeWSTicket(access); err == nil {
		t.Error("access token accepted as WS ticket")
	}
}

func TestDisableTOTPNeedsCode(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	u := mustUser(ctx, t, s, "erin", "viewer")
	enr, err := s.StartTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(enr.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.VerifyTOTPEnrollment(ctx, u.ID, code)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", "000000", "not-a-code"} {
		if err := s.DisableTOTPWithCode(ctx, u.ID, bad); !errors.Is(err, ErrMFAInvalidCode) {
			t.Errorf("code %q: err = %v, want ErrMFAInvalidCode", bad, err)
		}
	}
	if on, _ := s.HasMFA(ctx, u.ID); !on {
		t.Fatal("MFA turned off by a wrong code")
	}
	if err := s.DisableTOTPWithCode(ctx, u.ID, recovery[0]); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if on, _ := s.HasMFA(ctx, u.ID); on {
		t.Error("MFA still on")
	}

	// An enrollment that was never verified is cleared without a code.
	if _, err := s.StartTOTPEnrollment(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableTOTPWithCode(ctx, u.ID, ""); err != nil {
		t.Errorf("pending enrollment: %v", err)
	}
	var secret sql.NullString
	if err := database.QueryRow(`SELECT totp_secret FROM users WHERE id = ?`, u.ID).Scan(&secret); err != nil {
		t.Fatal(err)
	}
	if secret.Valid {
		t.Error("pending secret not cleared")
	}
}

func TestLoginUnknownUserDoesArgonWork(t *testing.T) {
	h := dummyHash()
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("dummy hash %q is not argon2id", h)
	}
	if ok, err := VerifyPassword("anything", h); err != nil || ok {
		t.Errorf("dummy hash verify: ok=%v err=%v", ok, err)
	}
	s, _ := newTestService(t)
	if _, err := s.Login(context.Background(), "nobody", "pw", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v", err)
	}
}

func TestRefreshParallelOnlyOneWins(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	mustUser(ctx, t, s, "dave", "viewer")
	res := mustLogin(ctx, t, s, "dave")

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.Refresh(ctx, res.RefreshToken); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of %d parallel refreshes with one token succeeded, want exactly 1", wins, n)
	}
}
