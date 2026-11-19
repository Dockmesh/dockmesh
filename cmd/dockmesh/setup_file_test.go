package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/dockmesh/dockmesh/internal/setup"
)

func TestSetupExampleParses(t *testing.T) {
	f, err := setup.ParseFile(strings.NewReader(setupExample))
	if err != nil {
		t.Fatalf("the example does not parse: %v", err)
	}
	if f.Admin == nil || f.Admin.PasswordFile == "" || f.Restore != nil {
		t.Fatalf("example is not a new install with a password file: %+v", f)
	}
}

func TestSetupFileValues(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good", "a-long-password-1\n")
	short := write("short", "too-short\n")
	two := write("two", "a-long-password-1\nsecond-line-here\n")
	same := write("same", "adminadminadmin\n")
	hookURL := write("hook", "https://hooks.example.org/T0/secret\n")
	tests := []struct {
		name    string
		file    setup.File
		wantErr string
	}{
		{"password file", setup.File{Admin: &setup.FileAdmin{PasswordFile: good}}, ""},
		{"short password", setup.File{Admin: &setup.FileAdmin{PasswordFile: short}}, "at least 12"},
		{"two lines", setup.File{Admin: &setup.FileAdmin{PasswordFile: two}}, "more than one line"},
		{"password is the username", setup.File{Admin: &setup.FileAdmin{Username: "adminadminadmin", PasswordFile: same}}, "username"},
		{"bad hash", setup.File{Admin: &setup.FileAdmin{PasswordHash: "plain"}}, "argon2id"},
		{"system data dir", setup.File{DataDir: "/etc/dm", Admin: &setup.FileAdmin{PasswordFile: good}}, "system directory"},
		{"restore needs no admin", setup.File{Restore: &setup.FileRestore{BackupFile: "/root/b"}}, ""},
		{"bad tls mode", setup.File{TLS: setup.FileTLS{Mode: "bogus"}, Restore: &setup.FileRestore{BackupFile: "/root/b"}}, "TLS_MODE"},
		{"network keys", setup.File{StacksDir: "/srv/stacks", Address: setup.FileAddress{AgentPort: 9443, AgentURL: "wss://dm.lan:9443/connect"},
			TLS: setup.FileTLS{Mode: "proxy", TrustedProxies: "10.0.0.2"}, Admin: &setup.FileAdmin{PasswordFile: good}}, ""},
		{"caddy with the proxy", setup.File{Address: setup.FileAddress{BaseURL: "https://dm.example.org"}, TLS: setup.FileTLS{Mode: "caddy"},
			Proxy: setup.FileProxy{ACMEEmail: "ops@example.org"}, Admin: &setup.FileAdmin{PasswordFile: good}}, ""},
		{"caddy without an ACME email", setup.File{Address: setup.FileAddress{BaseURL: "https://dm.example.org"}, TLS: setup.FileTLS{Mode: "caddy"},
			Admin: &setup.FileAdmin{PasswordFile: good}}, "ACME email"},
		{"notification with a secret file", setup.File{Notifications: []map[string]any{{"type": "slack", "name": "ops", "url_file": hookURL}},
			Admin: &setup.FileAdmin{PasswordFile: good}}, ""},
		{"notification with a missing file", setup.File{Notifications: []map[string]any{{"type": "slack", "url_file": filepath.Join(dir, "nope")}},
			Admin: &setup.FileAdmin{PasswordFile: good}}, "url_file"},
		{"agent port is the web port", setup.File{Address: setup.FileAddress{AgentPort: 8080}, Admin: &setup.FileAdmin{PasswordFile: good}}, "must differ"},
		{"stacks dir in /etc", setup.File{StacksDir: "/etc/stacks", Admin: &setup.FileAdmin{PasswordFile: good}}, "stacks dir"},
		{"trusted proxies without proxy mode", setup.File{TLS: setup.FileTLS{TrustedProxies: "10.0.0.2"}, Admin: &setup.FileAdmin{PasswordFile: good}}, "own reverse proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := setupFileValues(tt.file)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			case tt.wantErr == "" && len(tt.file.Notifications) > 0:
				if len(v.Notifications) != 1 || !strings.Contains(string(v.Notifications[0].Config), "hooks.example.org/T0/secret\"") {
					t.Fatalf("notifications %+v", v.Notifications)
				}
			case tt.wantErr == "" && tt.file.Admin != nil:
				if v.Admin.Username != "admin" || !strings.HasPrefix(v.Admin.PasswordHash, "$argon2id$") || strings.Contains(v.Admin.PasswordHash, "a-long-password") {
					t.Fatalf("values %+v", v.Admin)
				}
			}
		})
	}
}

func TestLoadSetupFileWarnsReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("a-long-password-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "setup.yaml")
	if err := os.WriteFile(cfg, []byte("admin:\n  password_file: "+pw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := loadSetupFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || strings.Contains(warnings[0], pw) || !strings.Contains(warnings[0], "admin.password_file") {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestApplySetupLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	data := t.TempDir()
	t.Setenv("DOCKMESH_DATA_DIR", data)
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte("a-long-password-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := setup.File{Admin: &setup.FileAdmin{Username: "ops", PasswordFile: pw}}
	v, err := setupFileValues(f)
	if err != nil {
		t.Fatal(err)
	}
	run := &setupFileRun{file: f, values: v}
	if _, err := applySetupLocal(context.Background(), run, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "data", "dockmesh.db")); err == nil {
		t.Fatal("the dry run created the database")
	}
	res, err := applySetupLocal(context.Background(), run, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Admin != "ops" {
		t.Errorf("admin = %q", res.Admin)
	}
	if n := countUsersReadOnly(filepath.Join(data, "data", "dockmesh.db"), true); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
	if _, err := applySetupLocal(context.Background(), run, false); err == nil || !strings.Contains(err.Error(), "never set up") {
		t.Errorf("second apply: %v", err)
	}
	other := setup.File{DataDir: "/srv/elsewhere", Admin: f.Admin}
	if _, err := applySetupLocal(context.Background(), &setupFileRun{file: other, values: v}, true); err == nil {
		t.Error("a data dir other than DOCKMESH_DATA_DIR was accepted")
	}
}

// The restore child reads the key root opened from the descriptor it
// inherits; reopening /dev/fd/N would check the file's mode again.
func TestReadBackupKeyFromDescriptor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte("AGE-SECRET-KEY-1TEST\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(f.Fd()))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, err := readBackupKey("", fd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "AGE-SECRET-KEY-1TEST\n" {
		t.Fatalf("key = %q", got)
	}
}

func TestWriteNewPrivateFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pw")
	if err := writeNewPrivateFile(p, "secret\n"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("stat %v %v", fi, err)
	}
	if err := writeNewPrivateFile(p, "other\n"); err == nil {
		t.Fatal("an existing file was overwritten")
	}
}
