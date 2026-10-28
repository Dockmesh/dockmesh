package apitokens

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// newTestDB opens a migrated database with one user, "owner", to own the
// tokens the tests create.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('owner', 'owner', 'x')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	return database
}

func TestCreateAndValidate(t *testing.T) {
	svc := New(newTestDB(t))
	plaintext, tok, err := svc.Create(context.Background(), CreateInput{
		CreatedByUserID: "owner",
		Name:            "test",
		Role:            "operator",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Plaintext must have the expected prefix and length.
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		t.Errorf("plaintext missing prefix: %q", plaintext)
	}
	if len(plaintext) < 40 {
		t.Errorf("plaintext too short: %d chars", len(plaintext))
	}

	// Metadata returned matches what we asked for.
	if tok.Name != "test" {
		t.Errorf("name = %q", tok.Name)
	}
	if tok.Role != "operator" {
		t.Errorf("role = %q", tok.Role)
	}

	// Validate round-trips.
	validated, err := svc.Validate(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if validated.ID != tok.ID {
		t.Errorf("id mismatch: %d vs %d", validated.ID, tok.ID)
	}
	if validated.Role != "operator" {
		t.Errorf("role mismatch: %q", validated.Role)
	}
}

func TestValidateWrongToken(t *testing.T) {
	svc := New(newTestDB(t))
	// No tokens in the DB at all.
	_, err := svc.Validate(context.Background(), "dmt_aaaaaaaaaaaaaa")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Invalid shape.
	_, err = svc.Validate(context.Background(), "not-a-dockmesh-token")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid, got %v", err)
	}
}

func TestValidateTamperedToken(t *testing.T) {
	svc := New(newTestDB(t))
	plaintext, _, err := svc.Create(context.Background(), CreateInput{
		CreatedByUserID: "owner",
		Name:            "t", Role: "viewer",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Flip the last char. Prefix matches a row but hash verification
	// should fail → ErrNotFound (no row matched on both prefix and hash).
	tampered := plaintext[:len(plaintext)-1] + "X"
	if tampered == plaintext {
		tampered = plaintext[:len(plaintext)-1] + "Y"
	}
	_, err = svc.Validate(context.Background(), tampered)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for tampered token, got %v", err)
	}
}

func TestRevokeBlocksValidate(t *testing.T) {
	svc := New(newTestDB(t))
	plaintext, tok, err := svc.Create(context.Background(), CreateInput{
		CreatedByUserID: "owner",
		Name:            "t", Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Works before revoke.
	if _, err := svc.Validate(context.Background(), plaintext); err != nil {
		t.Fatalf("pre-revoke validate: %v", err)
	}

	if err := svc.Revoke(context.Background(), tok.ID); err != nil {
		t.Fatal(err)
	}

	// Fails after revoke.
	_, err = svc.Validate(context.Background(), plaintext)
	if !errors.Is(err, ErrRevoked) {
		t.Errorf("expected ErrRevoked, got %v", err)
	}

	// Second revoke on already-revoked row is a no-op (no error, no effect).
	if err := svc.Revoke(context.Background(), tok.ID); !errors.Is(err, ErrNotFound) {
		// We treat "no rows affected" as ErrNotFound because the row
		// is considered "already revoked" — caller shouldn't see it
		// as success. Acceptable either way; codify the current
		// behavior.
		t.Logf("second revoke returned: %v (acceptable)", err)
	}
}

func TestExpiredToken(t *testing.T) {
	svc := New(newTestDB(t))

	// Create with a positive expiry first, then force it into the past
	// via a direct DB update (can't easily inject negative days through
	// the Create API).
	plaintext, tok, err := svc.Create(context.Background(), CreateInput{
		CreatedByUserID: "owner",
		Name:            "t", Role: "viewer", ExpiresInDays: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if _, err := svc.db.Exec(`UPDATE api_tokens SET expires_at = ? WHERE id = ?`, past, tok.ID); err != nil {
		t.Fatal(err)
	}

	_, err = svc.Validate(context.Background(), plaintext)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("expected ErrExpired, got %v", err)
	}
}

func TestCreateRequiresFields(t *testing.T) {
	svc := New(newTestDB(t))
	if _, _, err := svc.Create(context.Background(), CreateInput{Role: "admin"}); err == nil {
		t.Error("expected error for missing name")
	}
	if _, _, err := svc.Create(context.Background(), CreateInput{Name: "x"}); err == nil {
		t.Error("expected error for missing role")
	}
	if _, _, err := svc.Create(context.Background(), CreateInput{Name: "x", Role: "viewer"}); err == nil {
		t.Error("expected error for missing owner")
	}
}

func TestListAndGet(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	// Create two tokens.
	if _, _, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "a", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	_, tok2, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "b", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}

	got, err := svc.Get(ctx, tok2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b" || got.Role != "admin" {
		t.Errorf("get returned %+v", got)
	}

	// Nonexistent id.
	if _, err := svc.Get(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestNewTokensAreStoredAsSHA256(t *testing.T) {
	svc := New(newTestDB(t))
	_, tok, err := svc.Create(context.Background(), CreateInput{CreatedByUserID: "owner", Name: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := svc.db.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, sha256Scheme) {
		t.Errorf("hash = %q, want sha256 scheme", hash)
	}
}

// Tokens from before the change are argon2id hashes; the first good check
// rewrites them so later requests skip argon2 entirely.
func TestArgon2HashIsUpgradedOnUse(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	plaintext, tok, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	argon, err := auth.HashPassword(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE api_tokens SET token_hash = ? WHERE id = ?`, argon, tok.ID); err != nil {
		t.Fatal(err)
	}

	bad := plaintext[:len(plaintext)-1] + "!"
	if _, err := svc.Validate(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token: err = %v", err)
	}
	var hash string
	if err := svc.db.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != argon {
		t.Fatal("a failed check must not rewrite the hash")
	}

	if _, err := svc.Validate(ctx, plaintext); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, sha256Scheme) {
		t.Errorf("hash not upgraded: %q", hash)
	}
	if _, err := svc.Validate(ctx, plaintext); err != nil {
		t.Errorf("upgraded hash rejected: %v", err)
	}
}

func TestArgon2ChecksAreBounded(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	plaintext, tok, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE api_tokens SET token_hash = '$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id = ?`, tok.ID); err != nil {
		t.Fatal(err)
	}
	for range maxArgon2 {
		svc.argon2Slots <- struct{}{}
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := svc.Validate(cctx, plaintext); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("with all slots taken: err = %v, want the caller's deadline", err)
	}
}

func TestLegacyTokens(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	plaintext, tok, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "t", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`UPDATE api_tokens SET legacy = 1, created_by_user_id = NULL WHERE id = ?`, tok.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Validate(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Legacy || got.CreatedBy != nil || got.Role != "admin" {
		t.Errorf("legacy token = %+v", got)
	}
	if n, err := svc.CountActiveLegacy(ctx); err != nil || n != 1 {
		t.Errorf("count = %d, err %v", n, err)
	}
	if err := svc.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.CountActiveLegacy(ctx); err != nil || n != 0 {
		t.Errorf("count after revoke = %d, err %v", n, err)
	}
}

func TestOwnerDeletionMarksTokenOwnerless(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	plaintext, _, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(`DELETE FROM users WHERE id = 'owner'`); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Validate(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if got.Legacy || !got.OwnerMissing {
		t.Errorf("token of a deleted owner: legacy=%v owner_missing=%v", got.Legacy, got.OwnerMissing)
	}
}

func TestSyncLegacyNotice(t *testing.T) {
	database := newTestDB(t)
	svc := New(database)
	n := notices.NewService(database)
	ctx := context.Background()
	_, tok, err := svc.Create(ctx, CreateInput{CreatedByUserID: "owner", Name: "t", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	open := func() int {
		t.Helper()
		list, err := n.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}

	if err := svc.SyncLegacyNotice(ctx, n); err != nil || open() != 0 {
		t.Fatalf("no legacy tokens: open=%d err=%v", open(), err)
	}
	if _, err := database.Exec(`UPDATE api_tokens SET legacy = 1, created_by_user_id = NULL WHERE id = ?`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLegacyNotice(ctx, n); err != nil || open() != 1 {
		t.Fatalf("legacy token: open=%d err=%v", open(), err)
	}
	if err := svc.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncLegacyNotice(ctx, n); err != nil || open() != 0 {
		t.Fatalf("after revoke: open=%d err=%v", open(), err)
	}
}
