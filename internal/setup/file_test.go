package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseFile(t *testing.T) {
	tests := []struct {
		name, yaml string
		wantErr    string
	}{
		{"new install", "admin:\n  username: admin\n  password_file: /root/pw\n", ""},
		{"hash", "admin:\n  password_hash: $argon2id$x\n", ""},
		{"restore", "restore:\n  backup_file: /root/b.tar.gz\n  key_file: /root/k\n", ""},
		{"neither", "data_dir: /srv/dm\n", "needs either admin"},
		{"both", "admin:\n  password_file: /root/pw\nrestore:\n  backup_file: /root/b\n", "exclude each other"},
		{"plaintext password refused", "admin:\n  password: hunter2hunter2\n", "field password not found"},
		{"typo refused", "admin:\n  password_file: /root/pw\nadresse:\n  base_url: x\n", "field adresse not found"},
		{"no password", "admin:\n  username: admin\n", "exactly one of"},
		{"two passwords", "admin:\n  password_file: /root/pw\n  password_hash: $argon2id$x\n", "exactly one of"},
		{"relative password file", "admin:\n  password_file: pw\n", "absolute path"},
		{"relative backup", "restore:\n  backup_file: b.tar.gz\n", "absolute path"},
		{"relative key", "restore:\n  backup_file: /root/b\n  key_file: k\n", "absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFile(strings.NewReader(tt.yaml))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseFileTooLarge(t *testing.T) {
	big := "# " + strings.Repeat("x", maxSetupFile) + "\nadmin:\n  password_file: /root/pw\n"
	if _, err := ParseFile(strings.NewReader(big)); err == nil {
		t.Fatal("a file over the limit was accepted")
	}
}

func TestSecretFilesAndDataDir(t *testing.T) {
	f, err := ParseFile(strings.NewReader("restore:\n  backup_file: /root/b\n  key_file: /root/k\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.SecretFiles(), ","); got != "/root/b,/root/k" {
		t.Errorf("SecretFiles = %s", got)
	}
	if f.DataDirOrDefault() != DefaultFileDataDir {
		t.Errorf("DataDirOrDefault = %s", f.DataDirOrDefault())
	}
}

func TestReadableWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	dir := t.TempDir()
	for _, tt := range []struct {
		mode os.FileMode
		warn bool
	}{{0o600, false}, {0o400, false}, {0o640, true}, {0o644, true}, {0o604, true}} {
		p := filepath.Join(dir, "f")
		_ = os.Remove(p)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, tt.mode); err != nil {
			t.Fatal(err)
		}
		w := ReadableWarning(p)
		if (w != "") != tt.warn {
			t.Errorf("mode %04o: warning %q, want %v", tt.mode, w, tt.warn)
		}
		if tt.mode == 0o644 && !strings.Contains(w, "(mode 0644). Run: chmod 600 "+p) {
			t.Errorf("warning text %q", w)
		}
	}
}
