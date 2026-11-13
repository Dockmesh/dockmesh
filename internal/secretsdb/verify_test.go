package secretsdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

func sealedDB(t *testing.T) (*sql.DB, *secrets.Service) {
	t.Helper()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := targets.NewTargetStore(database, sec).Create(ctx, targets.TargetInput{Name: "s3", Type: "s3",
		Config: map[string]any{"endpoint": "e", "bucket": "b", "access_key": "a", "secret_key": "s3-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := globalenv.NewStore(database, sec).Create(ctx, globalenv.VarInput{Key: "TOKEN", Value: "tok", Encrypted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := globalenv.NewStore(database, sec).Create(ctx, globalenv.VarInput{Key: "PLAIN", Value: "p"}); err != nil {
		t.Fatal(err)
	}
	return database, sec
}

func TestVerifyAndHoldsPlaintext(t *testing.T) {
	tests := []struct {
		name       string
		change     string
		otherKey   bool
		wantVerify bool
		wantPlain  bool
	}{
		{"all sealed", "", false, true, false},
		{"plaintext target credential", `UPDATE backup_targets SET config_json = '{"bucket":"b","secret_key":"plain"}'`, false, false, true},
		{"plaintext job credential", `UPDATE backup_jobs SET target_config = '{"password":"plain"}'`, false, false, true},
		{"plaintext secret variable", `UPDATE global_env SET value = 'plain' WHERE key = 'TOKEN'`, false, false, true},
		{"before the encryption release", `DELETE FROM schema_migrations WHERE version LIKE '` + sealedSince + `%'`, false, true, true},
		{"sealed with another key", "", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, sec := sealedDB(t)
			ctx := context.Background()
			if tt.change != "" {
				if _, err := database.Exec(`INSERT INTO backup_jobs (name, target_type, target_config, sources) VALUES ('j', 'sftp', '{"host":"h"}', '[]')`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(tt.change); err != nil {
					t.Fatal(err)
				}
			}
			if tt.otherKey {
				var err error
				if sec, err = secrets.New(filepath.Join(t.TempDir(), "other"), true); err != nil {
					t.Fatal(err)
				}
			}
			if err := Verify(ctx, database, sec); (err == nil) != tt.wantVerify {
				t.Errorf("Verify = %v, want ok %v", err, tt.wantVerify)
			}
			plain, err := HoldsPlaintext(ctx, database)
			if err != nil {
				t.Fatal(err)
			}
			if plain != tt.wantPlain {
				t.Errorf("HoldsPlaintext = %v, want %v", plain, tt.wantPlain)
			}
		})
	}
}

func TestVerifyNeedsEncryption(t *testing.T) {
	database, _ := sealedDB(t)
	off, err := secrets.New(filepath.Join(t.TempDir(), "key"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), database, off); err == nil {
		t.Error("Verify passed with encryption disabled")
	}
}
