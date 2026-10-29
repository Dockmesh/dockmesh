package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func mustEncrypt(t *testing.T, s *Service, msg string) []byte {
	t.Helper()
	ct, err := s.Encrypt([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

func mustDecrypt(t *testing.T, s *Service, ct []byte, want string) {
	t.Helper()
	pt, err := s.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt %q: %v", want, err)
	}
	if string(pt) != want {
		t.Fatalf("decrypt = %q, want %q", pt, want)
	}
}

// Every point a rotation can stop at must leave a server that, after a
// restart, decrypts data written with the old key and with the new key.
func TestRotationSurvivesCrashAtEveryStep(t *testing.T) {
	steps := []string{"after-prepare", "after-reencrypt", "after-adopt"}
	for _, stop := range steps {
		t.Run(stop, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets.age-key")
			live, err := New(path, true)
			if err != nil {
				t.Fatal(err)
			}
			old := mustEncrypt(t, live, "old")

			next, err := live.RotateInMemory()
			if err != nil {
				t.Fatal(err)
			}
			if err := live.PrepareRotation(next); err != nil {
				t.Fatal(err)
			}
			var reencrypted []byte
			if stop != "after-prepare" {
				reencrypted = mustEncrypt(t, next, "new")
			}
			if stop == "after-adopt" {
				if _, err := live.AdoptAndPersist(next); err != nil {
					t.Fatal(err)
				}
			}

			restarted, err := New(path, true)
			if err != nil {
				t.Fatalf("restart: %v", err)
			}
			mustDecrypt(t, restarted, old, "old")
			if reencrypted != nil {
				mustDecrypt(t, restarted, reencrypted, "new")
			}
			wantCurrent := live.PublicRecipient()
			if got := restarted.PublicRecipient(); got != wantCurrent {
				t.Errorf("current key after restart = %s, want %s", got, wantCurrent)
			}
		})
	}
}

func TestRepeatedRotationsKeepAllKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age-key")
	live, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var cts [][]byte
	for i := range 3 {
		cts = append(cts, mustEncrypt(t, live, string(rune('a'+i))))
		next, err := live.RotateInMemory()
		if err != nil {
			t.Fatal(err)
		}
		if err := live.PrepareRotation(next); err != nil {
			t.Fatal(err)
		}
		if _, err := live.AdoptAndPersist(next); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	for i, ct := range cts {
		mustDecrypt(t, restarted, ct, string(rune('a'+i)))
	}

	export, err := restarted.ExportKeyFile()
	if err != nil {
		t.Fatal(err)
	}
	fromExport := filepath.Join(t.TempDir(), "imported.age-key")
	if err := os.WriteFile(fromExport, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	imported, err := New(fromExport, true)
	if err != nil {
		t.Fatal(err)
	}
	if imported.PublicRecipient() != restarted.PublicRecipient() {
		t.Error("exported key file doesn't load the current key first")
	}
	// A DR rebuild from the export alone must still read old backups.
	for i, ct := range cts {
		mustDecrypt(t, imported, ct, string(rune('a'+i)))
	}
}

// Installs that rotated with an older version have the previous key in
// "<key>.old"; it must still decrypt.
func TestLegacyOldKeyFileIsUsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.age-key")
	first, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	ct := mustEncrypt(t, first, "before")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	second, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	mustDecrypt(t, second, ct, "before")
}

// Stacks re-encrypted before a rotation failed must stay readable by the
// running server, without a restart.
func TestFailedRotationKeepsNewKeyUsable(t *testing.T) {
	live, err := New(filepath.Join(t.TempDir(), "secrets.age-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := live.RotateInMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := live.PrepareRotation(next); err != nil {
		t.Fatal(err)
	}
	mustDecrypt(t, live, mustEncrypt(t, next, "re-encrypted"), "re-encrypted")
}

func TestUnreadableLegacyOldFileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.age-key")
	if _, err := New(path, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".old", []byte("AGE-SECRET-KEY-1NOTAKEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, true); err != nil {
		t.Errorf("a broken .old file blocked startup: %v", err)
	}
}
