package secretsdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// After a rotation every stored secret opens with the new key alone.
func TestResealMovesSecretsToNewKey(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	old, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := targets.NewTargetStore(database, old).Create(ctx, targets.TargetInput{Name: "s3", Type: "s3",
		Config: map[string]any{"endpoint": "e", "bucket": "b", "access_key": "a", "secret_key": "s3-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := targets.NewTargetStore(database, old).Create(ctx, targets.TargetInput{Name: "local", Type: "local", Config: map[string]any{"path": "/srv"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := globalenv.NewStore(database, old).Create(ctx, globalenv.VarInput{Key: "TOKEN", Value: "tok", Encrypted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := globalenv.NewStore(database, old).Create(ctx, globalenv.VarInput{Key: "PLAIN", Value: "p"}); err != nil {
		t.Fatal(err)
	}

	next, err := old.RotateInMemory()
	if err != nil {
		t.Fatal(err)
	}
	n, err := Reseal(ctx, database, old, next)
	if err != nil || n != 2 {
		t.Fatalf("Reseal = %d, %v; want 2 rows", n, err)
	}

	list, err := targets.NewTargetStore(database, next).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range list {
		if tg.Type == "s3" && tg.Config.(map[string]any)["secret_key"] != "s3-secret" {
			t.Errorf("s3 target after rotation: %+v", tg)
		}
	}
	vars, err := globalenv.NewStore(database, next).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.Key == "TOKEN" && (v.Value != "tok" || v.Unreadable) {
			t.Errorf("global secret after rotation: %+v", v)
		}
	}
	var raw string
	if err := database.QueryRow(`SELECT value FROM global_env WHERE key = 'TOKEN'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := old.OpenString(raw); err == nil {
		t.Error("the previous key still opens the re-sealed value")
	}
}
