package globalenv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

func newTestStore(t *testing.T) (*Store, *secrets.Service) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(database, sec), sec
}

func storedValue(t *testing.T, s *Store, key string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT value FROM global_env WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSecretValuesStoredEncrypted(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	secret, err := s.Create(ctx, VarInput{Key: "DB_PASSWORD", Value: "pw-1", Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, VarInput{Key: "BASE_DOMAIN", Value: "haus.lan"}); err != nil {
		t.Fatal(err)
	}
	if raw := storedValue(t, s, "DB_PASSWORD"); strings.Contains(raw, "pw-1") || !secrets.IsSealed(raw) {
		t.Errorf("secret stored as %q", raw)
	}
	if raw := storedValue(t, s, "BASE_DOMAIN"); raw != "haus.lan" {
		t.Errorf("plain value stored as %q", raw)
	}
	if secret.Value != "pw-1" {
		t.Errorf("read back %q", secret.Value)
	}
	if _, err := s.Update(ctx, secret.ID, VarInput{Key: "DB_PASSWORD", Value: "pw-2", Encrypted: true}); err != nil {
		t.Fatal(err)
	}
	if raw := storedValue(t, s, "DB_PASSWORD"); strings.Contains(raw, "pw-2") {
		t.Errorf("updated secret stored as %q", raw)
	}
	merged, err := s.Merged(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "DB_PASSWORD=pw-2") || !strings.Contains(merged, "BASE_DOMAIN=haus.lan") {
		t.Errorf("merged env = %q", merged)
	}
}

func TestEncryptFlagged(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, row := range []struct {
		key, value string
		enc        int
	}{
		{"API_TOKEN", "tok-legacy", 1},
		{"EMPTY_SECRET", "", 1},
		{"PLAIN", "age:QUJD", 0},
	} {
		if _, err := s.db.Exec(`INSERT INTO global_env (key, value, group_name, encrypted) VALUES (?, ?, '', ?)`, row.key, row.value, row.enc); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.EncryptFlagged(ctx)
	if err != nil || n != 1 {
		t.Fatalf("EncryptFlagged = %d, %v; want 1, nil", n, err)
	}
	if raw := storedValue(t, s, "API_TOKEN"); strings.Contains(raw, "tok-legacy") {
		t.Errorf("legacy secret left as %q", raw)
	}
	if n, err := s.EncryptFlagged(ctx); err != nil || n != 0 {
		t.Errorf("second pass = %d, %v", n, err)
	}
	vars, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vars {
		got[v.Key] = v.Value
	}
	if got["API_TOKEN"] != "tok-legacy" || got["PLAIN"] != "age:QUJD" || got["EMPTY_SECRET"] != "" {
		t.Errorf("values after migration = %v", got)
	}
}
