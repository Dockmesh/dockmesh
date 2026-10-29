package gitsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

const testSecret = "0123456789abcdef-webhook"

func newWebhookTestService(t *testing.T, encrypt bool) (*Service, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	var sec *secrets.Service
	if encrypt {
		if sec, err = secrets.New(filepath.Join(dir, "secrets.age-key"), true); err != nil {
			t.Fatal(err)
		}
	}
	return New(database, sec, nil, t.TempDir(), nil), database
}

func storedWebhookSecret(t *testing.T, database *sql.DB, stack string) string {
	t.Helper()
	var v sql.NullString
	if err := database.QueryRow(`SELECT webhook_secret FROM stack_git_sources WHERE stack_name = ?`, stack).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func TestWebhookSecretRoundTrip(t *testing.T) {
	tests := []struct {
		name          string
		encrypt       bool
		wantEncrypted bool
	}{
		{"encryption enabled", true, true},
		{"encryption disabled", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, database := newWebhookTestService(t, tc.encrypt)
			src, err := svc.Configure(ctx, "app", Input{RepoURL: "https://example.com/app.git", WebhookSecret: testSecret})
			if err != nil {
				t.Fatal(err)
			}
			if !src.HasWebhookSecret {
				t.Fatal("HasWebhookSecret = false after setting a secret")
			}
			out, err := json.Marshal(src)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), testSecret) {
				t.Fatalf("source JSON leaks the secret: %s", out)
			}

			stored := storedWebhookSecret(t, database, "app")
			if encrypted := strings.HasPrefix(stored, encryptedWebhookPrefix); encrypted != tc.wantEncrypted {
				t.Fatalf("stored value %q encrypted = %v, want %v", stored, encrypted, tc.wantEncrypted)
			}
			if tc.wantEncrypted && strings.Contains(stored, testSecret) {
				t.Fatalf("stored value contains the plaintext secret: %q", stored)
			}

			got, err := svc.WebhookSecret(ctx, "app")
			if err != nil || got != testSecret {
				t.Fatalf("WebhookSecret = %q, %v; want %q", got, err, testSecret)
			}
			names, err := svc.stacksWithoutWebhookSecret(ctx)
			if err != nil || len(names) != 0 {
				t.Fatalf("stacks without secret = %v, %v; want none", names, err)
			}
		})
	}
}

func TestLegacyPlaintextWebhookSecret(t *testing.T) {
	ctx := context.Background()
	svc, database := newWebhookTestService(t, true)
	for _, stack := range []string{"legacy", "fresh", "none"} {
		if _, err := svc.Configure(ctx, stack, Input{RepoURL: "https://example.com/" + stack + ".git"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`UPDATE stack_git_sources SET webhook_secret = ? WHERE stack_name = 'legacy'`, testSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ctx, "fresh", Input{RepoURL: "https://example.com/fresh.git", WebhookSecret: testSecret + "-fresh"}); err != nil {
		t.Fatal(err)
	}
	freshStored := storedWebhookSecret(t, database, "fresh")

	if got, err := svc.WebhookSecret(ctx, "legacy"); err != nil || got != testSecret {
		t.Fatalf("legacy WebhookSecret before upgrade = %q, %v", got, err)
	}

	n, err := svc.EncryptLegacyWebhookSecrets(ctx)
	if err != nil || n != 1 {
		t.Fatalf("EncryptLegacyWebhookSecrets = %d, %v; want 1", n, err)
	}
	if stored := storedWebhookSecret(t, database, "legacy"); !strings.HasPrefix(stored, encryptedWebhookPrefix) || strings.Contains(stored, testSecret) {
		t.Fatalf("legacy row not encrypted: %q", stored)
	}
	if stored := storedWebhookSecret(t, database, "fresh"); stored != freshStored {
		t.Fatal("already encrypted row was rewritten")
	}
	if stored := storedWebhookSecret(t, database, "none"); stored != "" {
		t.Fatalf("row without secret got %q", stored)
	}

	tests := []struct {
		stack string
		want  string
	}{
		{"legacy", testSecret},
		{"fresh", testSecret + "-fresh"},
		{"none", ""},
	}
	for _, tc := range tests {
		if got, err := svc.WebhookSecret(ctx, tc.stack); err != nil || got != tc.want {
			t.Errorf("%s: WebhookSecret = %q, %v; want %q", tc.stack, got, err, tc.want)
		}
	}

	if n, err := svc.EncryptLegacyWebhookSecrets(ctx); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; want 0", n, err)
	}
}
