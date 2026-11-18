package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/setup"
)

func TestMaterializeAgentURLs(t *testing.T) {
	tests := []struct {
		name      string
		listen    string
		agentURL  string
		wantAgent string
	}{
		{name: "derived from the port", listen: ":9443", wantAgent: "wss://dm.lan:9443/connect"},
		{name: "set by hand", listen: ":9443", agentURL: "wss://agents.example.org:443/connect", wantAgent: "wss://agents.example.org:443/connect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := setup.ApplyValues{DataDir: filepath.Join(t.TempDir(), "dm"), BaseURL: "https://dm.lan:8080", AgentListen: tt.listen, AgentURL: tt.agentURL}
			v.Admin.Username = "admin"
			v.Admin.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
			if err := os.MkdirAll(v.DataDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := materialize(context.Background(), v, ""); err != nil {
				t.Fatal(err)
			}
			conn, err := db.Open(filepath.Join(v.DataDir, "data", "dockmesh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			st := settings.NewStore(conn)
			if err := st.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := st.Get(settings.KeyAgentPublicURL, ""); got != tt.wantAgent {
				t.Errorf("agent URL %q, want %q", got, tt.wantAgent)
			}
			if fi, err := os.Stat(filepath.Join(v.DataDir, "stacks")); err != nil || !fi.IsDir() {
				t.Errorf("stacks dir: %v", err)
			}
		})
	}
}
