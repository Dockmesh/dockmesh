package settings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

// DOCKMESH_PROXY_ENABLED only sets the first-start default: once the
// proxy is disabled in the UI, the next start keeps it disabled.
func TestLoadEnvOnlyFillsUnsavedValues(t *testing.T) {
	t.Setenv("DOCKMESH_PROXY_ENABLED", "true")
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}

	st := NewStore(database)
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.Get(KeyProxyEnabled, ""); got != "true" {
		t.Fatalf("first start: proxy_enabled = %q, want the env default true", got)
	}
	if err := st.Set(ctx, KeyProxyEnabled, "false"); err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(database)
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Get(KeyProxyEnabled, ""); got != "false" {
		t.Fatalf("after disable + restart: proxy_enabled = %q, want false", got)
	}
}
