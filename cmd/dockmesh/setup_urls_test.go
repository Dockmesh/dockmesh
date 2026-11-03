package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestSaveSetupURLs(t *testing.T) {
	tests := []struct {
		name      string
		prevBase  string
		prevAgent string
		publicURL string
		wantAgent string
	}{
		{"fresh install", "http://localhost:8080", "", "https://dm.lan", "wss://dm.lan:8443/connect"},
		{"derived URL follows the new base", "https://old.lan", "wss://old.lan:8443/connect", "https://dm.lan", "wss://dm.lan:8443/connect"},
		{"hand-set agent URL is kept", "https://old.lan", "wss://agents.example.com:443/connect", "https://dm.lan", "wss://agents.example.com:443/connect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DOCKMESH_BASE_URL", "")
			t.Setenv("DOCKMESH_AGENT_PUBLIC_URL", "")
			database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			st := settings.NewStore(database)
			if err := st.Load(ctx); err != nil {
				t.Fatal(err)
			}
			if err := st.Set(ctx, settings.KeyBaseURL, tt.prevBase); err != nil {
				t.Fatal(err)
			}
			if err := st.Set(ctx, settings.KeyAgentPublicURL, tt.prevAgent); err != nil {
				t.Fatal(err)
			}
			if err := saveSetupURLs(ctx, st, tt.publicURL, ":8443"); err != nil {
				t.Fatal(err)
			}
			if got := st.Get(settings.KeyBaseURL, ""); got != tt.publicURL {
				t.Errorf("base_url = %q, want %q", got, tt.publicURL)
			}
			if got := st.Get(settings.KeyAgentPublicURL, ""); got != tt.wantAgent {
				t.Errorf("agent_public_url = %q, want %q", got, tt.wantAgent)
			}
		})
	}
}

// Invite links follow the Base URL the wizard or the settings page saved;
// DOCKMESH_BASE_URL overrides it, and without either the default is used.
func TestInviteLinksUseThePublicBaseURL(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := settings.NewStore(database)
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BaseURL: "http://localhost:8080", AgentListen: ":8443"}
	inv := invites.New(database, cfg.BaseURL)
	inv.SetBaseURL(func() string { return agentEndpoints(cfg, st).BaseURL })

	steps := []struct {
		name, env, setting, want string
	}{
		{"nothing set", "", "", "http://localhost:8080/invite/"},
		{"wizard URL", "", "https://dm.lan", "https://dm.lan/invite/"},
		{"changed in settings", "", "https://dockmesh.example.com/", "https://dockmesh.example.com/invite/"},
		{"env overrides", "https://env.example.com", "https://dockmesh.example.com", "https://env.example.com/invite/"},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			t.Setenv("DOCKMESH_BASE_URL", s.env)
			t.Setenv("DOCKMESH_AGENT_PUBLIC_URL", "")
			if s.setting != "" {
				if err := saveSetupURLs(ctx, st, s.setting, cfg.AgentListen); err != nil {
					t.Fatal(err)
				}
			}
			got, err := inv.Create(ctx, invites.CreateInput{Role: "viewer"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got.AcceptURL, s.want) || strings.TrimPrefix(got.AcceptURL, s.want) != got.Token {
				t.Fatalf("accept URL %q, want %s<token>", got.AcceptURL, s.want)
			}
		})
	}
}
