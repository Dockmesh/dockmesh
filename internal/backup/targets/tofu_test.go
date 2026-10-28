package targets

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
)

func TestKeepHostKey(t *testing.T) {
	stored := map[string]any{"host": "a.example", "port": 22, "host_key": "ssh-ed25519 OLD", "host_key_pending": "ssh-ed25519 NEW"}
	tests := []struct {
		name        string
		oldType     string
		oldCfg      any
		newCfg      map[string]any
		wantKey     any
		wantPending any
	}{
		{"same server keeps keys", "sftp", stored, map[string]any{"host": "a.example"}, "ssh-ed25519 OLD", "ssh-ed25519 NEW"},
		{"client key ignored", "sftp", stored, map[string]any{"host": "a.example", "host_key": "ssh-ed25519 EVIL"}, "ssh-ed25519 OLD", "ssh-ed25519 NEW"},
		{"other host drops keys", "sftp", stored, map[string]any{"host": "b.example", "host_key": "ssh-ed25519 OLD"}, nil, nil},
		{"other port drops keys", "sftp", stored, map[string]any{"host": "a.example", "port": 2222}, nil, nil},
		{"new target", "", nil, map[string]any{"host": "a.example", "host_key": "ssh-ed25519 EVIL"}, nil, nil},
		{"type change", "s3", map[string]any{"host_key": "x"}, map[string]any{"host": "a.example"}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := KeepHostKey(tc.oldType, tc.oldCfg, "sftp", tc.newCfg).(map[string]any)
			if !ok {
				t.Fatalf("not a map")
			}
			if got["host_key"] != tc.wantKey || got["host_key_pending"] != tc.wantPending {
				t.Errorf("got host_key=%v pending=%v, want %v %v", got["host_key"], got["host_key_pending"], tc.wantKey, tc.wantPending)
			}
		})
	}
}

func TestStoredTargetHostKey(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := NewTargetStore(database)
	n := notices.NewService(database)
	tgt, err := st.Create(ctx, TargetInput{Name: "offsite", Type: "sftp", Config: map[string]any{"host": "a.example", "username": "u"}})
	if err != nil {
		t.Fatal(err)
	}
	tf := TOFU{Rec: JSONColumnRecorder{DB: database, Table: "backup_targets", Column: "config_json", ID: tgt.ID}, Notices: n, ID: "target:1", Label: "t"}

	if err := tf.Handle(ctx, HostKeyEvent{Kind: "recorded", Key: "ssh-ed25519 ONE"}); err != nil {
		t.Fatal(err)
	}
	var changed *HostKeyChangedError
	if err := tf.Handle(ctx, HostKeyEvent{Kind: "recorded", Key: "ssh-ed25519 EVIL"}); !errors.As(err, &changed) {
		t.Fatalf("racing record: err = %v, want HostKeyChangedError", err)
	}

	// Editing the target must neither drop nor replace the recorded key.
	if _, err := st.Update(ctx, tgt.ID, TargetInput{Name: "offsite", Type: "sftp", Config: map[string]any{"host": "a.example", "username": "v"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptHostKey(ctx, tgt.ID, n); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, tgt.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := got.Config.(map[string]any)
	if cfg["host_key"] != "ssh-ed25519 EVIL" || cfg["host_key_pending"] != nil || cfg["username"] != "v" {
		t.Errorf("after accept: %v", cfg)
	}
	if err := st.AcceptHostKey(ctx, tgt.ID, n); !errors.Is(err, ErrNoPendingHostKey) {
		t.Errorf("second accept: err = %v", err)
	}
}
