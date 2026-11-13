package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestCompletionsKnowEveryCommand(t *testing.T) {
	for name, script := range map[string]string{"bash": bashCompletion, "zsh": zshCompletion, "fish": fishCompletion} {
		for _, want := range []string{"init", "reissue-server"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s completion lacks %s", name, want)
			}
		}
	}
}

func TestSeedScannerEnabled(t *testing.T) {
	tests := []struct {
		name, stored string
		envEnabled   bool
		want         string
	}{
		{"empty takes the env default on", "", true, "true"},
		{"empty takes the env default off", "", false, "false"},
		{"saved off beats env on", "false", true, "false"},
		{"saved on beats env off", "true", false, "true"},
		{"other env spellings are normalized", "0", true, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			ctx := context.Background()
			store := settings.NewStore(database)
			if err := store.Set(ctx, settings.KeyScannerEnabled, tt.stored); err != nil {
				t.Fatal(err)
			}
			if err := seedScannerEnabled(ctx, store, tt.envEnabled); err != nil {
				t.Fatal(err)
			}
			if got := store.Stored(settings.KeyScannerEnabled); got != tt.want {
				t.Fatalf("stored = %q, want %q", got, tt.want)
			}
		})
	}
}
