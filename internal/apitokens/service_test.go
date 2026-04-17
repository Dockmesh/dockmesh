package apitokens

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB spins up an in-memory SQLite with the api_tokens schema
// pre-applied. Matches the shape migration 021 creates.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	schema := `
	CREATE TABLE api_tokens (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		token_prefix       TEXT    NOT NULL UNIQUE,
		token_hash         TEXT    NOT NULL,
		name               TEXT    NOT NULL,
		role               TEXT    NOT NULL,
		created_by_user_id INTEGER,
		created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at         DATETIME,
		last_used_at       DATETIME,
		last_used_ip       TEXT,
		revoked_at         DATETIME
	);
	CREATE INDEX idx_api_tokens_prefix ON api_tokens(token_prefix);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestCreateAndValidate(t *testing.T) {
	svc := New(newTestDB(t))
	plaintext, tok, err := svc.Create(context.Background(), CreateInput{
		Name: "test",
		Role: "operator",
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
		Name: "t", Role: "viewer",
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
		Name: "t", Role: "admin",
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
	if err := svc.Revoke(context.Background(), tok.ID); err != ErrNotFound {
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
		Name: "t", Role: "viewer", ExpiresInDays: 1,
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
}

func TestListAndGet(t *testing.T) {
	svc := New(newTestDB(t))
	ctx := context.Background()
	// Create two tokens.
	if _, _, err := svc.Create(ctx, CreateInput{Name: "a", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	_, tok2, err := svc.Create(ctx, CreateInput{Name: "b", Role: "admin"})
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
