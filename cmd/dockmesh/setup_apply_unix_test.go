//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/setup"
)

func TestOnlySetupLeftovers(t *testing.T) {
	tests := []struct {
		name    string
		same    bool
		entries []string // trailing / is a directory
		ok      bool
	}{
		{name: "empty", ok: true},
		{name: "installer dirs", entries: []string{"data/", "stacks/"}, ok: true},
		{name: "own state and update dirs", same: true, entries: []string{".setup/", "update/", "data/", "stacks/"}, ok: true},
		{name: "state dir of another data dir", entries: []string{".setup/"}},
		{name: "failed apply set aside", entries: []string{"data/", "data/dockmesh.db.failed-20261114T220000Z"}, ok: true},
		{name: "a database", entries: []string{"data/", "data/dockmesh.db"}},
		{name: "a stack", entries: []string{"stacks/", "stacks/web/"}},
		{name: "other files", entries: []string{"notes.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, e := range tt.entries {
				p := filepath.Join(dir, e)
				var err error
				if e[len(e)-1] == '/' {
					err = os.MkdirAll(p, 0o750)
				} else {
					err = os.WriteFile(p, nil, 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			a := &setupApplier{dataDir: "/elsewhere"}
			if tt.same {
				a.dataDir = dir
			}
			if err := a.onlySetupLeftovers(dir); (err == nil) != tt.ok {
				t.Errorf("onlySetupLeftovers: %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestMaterialize(t *testing.T) {
	base := t.TempDir()
	pkiDir := setup.PKIDir(base)
	m, err := pki.New(pkiDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := m.EnsureUICert(nil)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("a-long-password-1")
	if err != nil {
		t.Fatal(err)
	}
	var v setup.ApplyValues
	v.DataDir = filepath.Join(t.TempDir(), "dm")
	v.BaseURL = "https://dm.lan:8080"
	v.Admin.Username = "admin"
	v.Admin.PasswordHash = hash
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(v.DataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := materialize(context.Background(), v, pkiDir); err != nil {
		t.Fatal(err)
	}
	moved, err := pki.ReadUICert(filepath.Join(v.DataDir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if pki.Fingerprint(moved.Raw) != pki.Fingerprint(cert.Leaf.Raw) {
		t.Error("the certificate changed with the move")
	}
	conn, err := db.Open(filepath.Join(v.DataDir, "data", "dockmesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'admin' AND password = ?`, hash).Scan(&n); err != nil || n != 1 {
		t.Fatalf("admin rows %d, %v", n, err)
	}
	if err := materialize(context.Background(), v, pkiDir); err == nil {
		t.Error("a second materialize ran over the existing database")
	}
}

func TestRootOwnedAncestors(t *testing.T) {
	if err := rootOwnedAncestors("/"); err != nil {
		t.Errorf("/: %v", err)
	}
	if os.Geteuid() != 0 {
		if err := rootOwnedAncestors(t.TempDir()); err == nil {
			t.Error("a directory of another account passed")
		}
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/", link); err != nil {
		t.Skip(err)
	}
	if err := rootOwnedAncestors(link); err == nil {
		t.Error("a symlink passed")
	}
}
