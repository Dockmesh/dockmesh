package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// A restored database keeps the old server's addresses; the Base URL of
// the setup file replaces them, and applying it again changes nothing.
func TestSetRestoredBaseURL(t *testing.T) {
	tests := []struct {
		name, prevAgent, wantAgent string
	}{
		{"derived agent URL follows", "wss://old.lan:8443/connect", "wss://new.lan:8443/connect"},
		{"hand-set agent URL is kept", "wss://agents.example.com:443/connect", "wss://agents.example.com:443/connect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dockmesh.db")
			database, err := dbtest.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			st := settings.NewStore(database)
			if err := st.Load(ctx); err != nil {
				t.Fatal(err)
			}
			for k, v := range map[string]string{settings.KeyBaseURL: "https://old.lan", settings.KeyAgentPublicURL: tt.prevAgent} {
				if err := st.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			database.Close()

			for range 2 {
				if err := setRestoredBaseURL(ctx, &config.Config{DBPath: path, AgentListen: ":8443"}, "https://new.lan"); err != nil {
					t.Fatal(err)
				}
			}
			database, err = dbtest.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			st = settings.NewStore(database)
			if err := st.Load(ctx); err != nil {
				t.Fatal(err)
			}
			if got := st.Get(settings.KeyBaseURL, ""); got != "https://new.lan" {
				t.Errorf("base URL %q", got)
			}
			if got := st.Get(settings.KeyAgentPublicURL, ""); got != tt.wantAgent {
				t.Errorf("agent URL %q, want %q", got, tt.wantAgent)
			}
		})
	}
}

func TestSetRestoredBaseURLRefusesInvalid(t *testing.T) {
	cfg := &config.Config{DBPath: filepath.Join(t.TempDir(), "dockmesh.db"), AgentListen: ":8443"}
	if err := setRestoredBaseURL(context.Background(), cfg, "https://admin@new.lan"); err == nil {
		t.Fatal("a base URL with user info was saved")
	}
}
