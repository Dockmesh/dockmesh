package backup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

func newTestService(t *testing.T, encrypt bool) (*Service, *notices.Service) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), encrypt)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(database, nil, nil, sec, SystemPaths{})
	n := notices.NewService(database)
	svc.SetNotices(n)
	return svc, n
}

func sftpJob() JobInput {
	return JobInput{
		Name:         "offsite",
		TargetType:   "sftp",
		TargetConfig: map[string]any{"host": "backup.example", "username": "u"},
		Sources:      []Source{{Type: "volume", Name: "pgdata"}},
	}
}

func TestSFTPHostKeyLifecycle(t *testing.T) {
	svc, n := newTestService(t, true)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, sftpJob())
	if err != nil {
		t.Fatal(err)
	}

	handleHostKey(ctx, svc.store, n, job, targets.HostKeyEvent{Kind: "recorded", Key: "ssh-ed25519 AAAA1", Fingerprint: "SHA256:one"})
	got, _ := svc.GetJob(ctx, job.ID)
	cfg, _ := configMap(got.TargetConfig)
	if cfg["host_key"] != "ssh-ed25519 AAAA1" {
		t.Fatalf("first key not recorded: %v", cfg)
	}
	if cfg["username"] != "u" {
		t.Errorf("recording the key dropped other config: %v", cfg)
	}

	if err := svc.AcceptHostKey(ctx, job.ID); !errors.Is(err, ErrNoPendingHostKey) {
		t.Errorf("accept without a pending key: %v", err)
	}

	handleHostKey(ctx, svc.store, n, job, targets.HostKeyEvent{Kind: "changed", Key: "ssh-ed25519 AAAA2", Fingerprint: "SHA256:two", OldFingerprint: "SHA256:one"})
	open, _ := n.Open(ctx)
	if !hasKey(open, hostKeyChangedKey(job.ID)) {
		t.Fatal("changed key raised no notice")
	}
	if err := svc.AcceptHostKey(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.GetJob(ctx, job.ID)
	cfg, _ = configMap(got.TargetConfig)
	if cfg["host_key"] != "ssh-ed25519 AAAA2" || cfg["host_key_pending"] != nil {
		t.Errorf("after accept: %v", cfg)
	}
	open, _ = n.Open(ctx)
	if hasKey(open, hostKeyChangedKey(job.ID)) {
		t.Error("changed-key notice still open after accept")
	}
}

func TestEncryptNeedsSecrets(t *testing.T) {
	svc, _ := newTestService(t, false)
	ctx := context.Background()
	in := sftpJob()
	in.Encrypt = true
	if _, err := svc.CreateJob(ctx, in); !errors.Is(err, ErrEncryptUnavailable) {
		t.Fatalf("create: err = %v, want ErrEncryptUnavailable", err)
	}
	in.Encrypt = false
	in.Enabled = true
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Encrypt = true
	if _, err := svc.UpdateJob(ctx, job.ID, in); !errors.Is(err, ErrEncryptUnavailable) {
		t.Fatalf("update: err = %v, want ErrEncryptUnavailable", err)
	}
	if _, err := svc.store.db.Exec(`UPDATE backup_jobs SET encrypt = 1 WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	names, err := svc.UnencryptableJobs(ctx)
	if err != nil || len(names) != 1 || names[0] != "offsite" {
		t.Errorf("unencryptable = %v, %v", names, err)
	}
}

func hasKey(ns []notices.Notice, key string) bool {
	for _, n := range ns {
		if n.Key == key {
			return true
		}
	}
	return false
}
