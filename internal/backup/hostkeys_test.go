package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// The job's SFTP settings were saved as a target; keys live there.
	stored := targets.NewTargetStore(svc.store.db)
	targetConfig := func() map[string]any {
		t, err := stored.Get(ctx, job.TargetID)
		if err != nil {
			return nil
		}
		m, _ := configMap(t.Config)
		return m
	}
	id := fmt.Sprintf("target:%d", job.TargetID)
	tofu := targets.TOFU{Rec: targets.TargetKeyRecorder(svc.store.db, job.TargetID), Notices: n, ID: id}
	if err := tofu.Handle(ctx, targets.HostKeyEvent{Kind: "recorded", Key: "ssh-ed25519 AAAA1", Fingerprint: "SHA256:one"}); err != nil {
		t.Fatal(err)
	}
	cfg := targetConfig()
	if cfg["host_key"] != "ssh-ed25519 AAAA1" {
		t.Fatalf("first key not recorded: %v", cfg)
	}
	if cfg["username"] != "u" {
		t.Errorf("recording the key dropped other config: %v", cfg)
	}

	if err := svc.AcceptHostKey(ctx, job.ID); !errors.Is(err, ErrNoPendingHostKey) {
		t.Errorf("accept without a pending key: %v", err)
	}

	// A racing first connection with another key must not overwrite it.
	var changed *targets.HostKeyChangedError
	if err := tofu.Handle(ctx, targets.HostKeyEvent{Kind: "recorded", Key: "ssh-ed25519 EVIL", Fingerprint: "SHA256:evil"}); !errors.As(err, &changed) {
		t.Fatalf("racing record: err = %v, want HostKeyChangedError", err)
	}

	_ = tofu.Handle(ctx, targets.HostKeyEvent{Kind: "changed", Key: "ssh-ed25519 AAAA2", Fingerprint: "SHA256:two", OldFingerprint: "SHA256:one"})
	open, _ := n.Open(ctx)
	changedKey := targets.ChangedNoticeKey(id)
	if !hasKey(open, changedKey) {
		t.Fatal("changed key raised no notice")
	}
	if err := svc.AcceptHostKey(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	cfg = targetConfig()
	if cfg["host_key"] != "ssh-ed25519 AAAA2" || cfg["host_key_pending"] != nil {
		t.Errorf("after accept: %v", cfg)
	}
	open, _ = n.Open(ctx)
	if hasKey(open, changedKey) {
		t.Error("changed-key notice still open after accept")
	}
}

func TestEncryptNeedsSecrets(t *testing.T) {
	svc, _ := newTestService(t, false)
	ctx := context.Background()
	in := sftpJob()
	in.Encrypt = true
	in.Enabled = true
	if _, err := svc.CreateJob(ctx, in); !errors.Is(err, ErrEncryptUnavailable) {
		t.Fatalf("create: err = %v, want ErrEncryptUnavailable", err)
	}
	in.Encrypt = false
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Encrypt = true
	if _, err := svc.UpdateJob(ctx, job.ID, in); !errors.Is(err, ErrEncryptUnavailable) {
		t.Fatalf("update: err = %v, want ErrEncryptUnavailable", err)
	}
	in.Enabled = false
	if _, err := svc.UpdateJob(ctx, job.ID, in); err != nil {
		t.Errorf("disabling an encrypting job must stay possible: %v", err)
	}
	in.Enabled = true
	if _, err := svc.store.db.Exec(`UPDATE backup_jobs SET encrypt = 1, enabled = 1 WHERE id = ?`, job.ID); err != nil {
		t.Fatal(err)
	}
	names, err := svc.UnencryptableJobs(ctx)
	if err != nil || len(names) != 1 || names[0] != "offsite" {
		t.Errorf("unencryptable = %v, %v", names, err)
	}
}

func configMap(cfg any) (map[string]any, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if string(b) != "null" {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func hasKey(ns []notices.Notice, key string) bool {
	for _, n := range ns {
		if n.Key == key {
			return true
		}
	}
	return false
}

func TestInlineUpdateKeepsHostKey(t *testing.T) {
	svc, _ := newTestService(t, false)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, sftpJob())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.db.Exec(`UPDATE backup_targets SET config_json = json_set(config_json, '$.host_key', 'ssh-ed25519 PINNED') WHERE id = ?`, job.TargetID); err != nil {
		t.Fatal(err)
	}
	in := sftpJob()
	in.Schedule = "0 4 * * *"
	got, err := svc.UpdateJob(ctx, job.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := targets.NewTargetStore(svc.store.db).Get(ctx, got.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := configMap(tgt.Config); cfg["host_key"] != "ssh-ed25519 PINNED" {
		t.Fatalf("pinned host key lost on update: %v", cfg)
	}
}
