package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/pki"
)

func TestCAReissueServer(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		wantOut  []string
		wantSANs []string
	}{
		{
			name:     "repeated and comma separated flags",
			args:     []string{"--add-san", "203.0.113.5", "--add-san", "dr.example.com,2001:db8::7"},
			wantOut:  []string{"added:  dr.example.com, 203.0.113.5, 2001:db8::7", "restart the server", "DOCKMESH_AGENT_URL"},
			wantSANs: []string{"203.0.113.5", "dr.example.com", "2001:db8::7", "old.example.com"},
		},
		{
			name:     "already covered",
			args:     []string{"--add-san", "old.example.com"},
			wantOut:  []string{"nothing changed"},
			wantSANs: []string{"old.example.com"},
		},
		{name: "missing flag", args: nil, wantErr: true},
		{name: "positional argument", args: []string{"203.0.113.5"}, wantErr: true},
		{name: "url instead of host", args: []string{"--add-san", "wss://203.0.113.5:8443/connect"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DOCKMESH_DB_PATH", filepath.Join(dir, "dockmesh.db"))
			t.Setenv("DOCKMESH_SECRETS_PATH", filepath.Join(dir, "secrets.env"))
			t.Setenv("DOCKMESH_AGENT_PUBLIC_URL", "")
			if _, err := pki.New(dir, []string{"old.example.com"}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := caReissueServer(tt.args, &out)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, output:\n%s", out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.wantOut {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			m, err := pki.New(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range tt.wantSANs {
				if !m.ServerCertCovers(h) {
					t.Errorf("cert does not cover %q", h)
				}
			}
		})
	}
}
