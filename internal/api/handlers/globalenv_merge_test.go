package handlers

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// A deploy must not go ahead without a global secret it can't decrypt;
// compose would fall back to defaults without saying so.
func TestMergeGlobalEnvRefusesUnreadableSecret(t *testing.T) {
	db := openRedactionTestDB(t)
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := globalenv.NewStore(db, sec).Create(ctx, globalenv.VarInput{Key: "DB_PASSWORD", Value: "pw", Encrypted: true}); err != nil {
		t.Fatal(err)
	}

	got, err := (&Handlers{GlobalEnv: globalenv.NewStore(db, sec)}).mergeGlobalEnv(ctx, "A=1")
	if err != nil || !strings.Contains(got, "DB_PASSWORD=pw") || !strings.HasSuffix(got, "A=1") {
		t.Fatalf("merge = %q, %v", got, err)
	}
	if _, err := (&Handlers{GlobalEnv: globalenv.NewStore(db, nil)}).mergeGlobalEnv(ctx, "A=1"); err == nil || !strings.Contains(err.Error(), "DB_PASSWORD") {
		t.Fatalf("merge without the key: err = %v", err)
	}
}
