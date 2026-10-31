package handlers

import (
	"context"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/redact"
)

func TestJobTargetSecrets(t *testing.T) {
	ctx := context.Background()
	store := targets.NewTargetStore(openRedactionTestDB(t))
	saved, err := store.Create(ctx, targets.TargetInput{
		Name: "nas", Type: "smb",
		Config: map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "s3cret", "path": "backups"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// What the wizard copies from GET /backups/targets.
	redacted := redact.ToMap(redactTarget(saved).Config)
	if redacted["password"] != "" || redacted["password_set"] != true {
		t.Fatalf("target not redacted: %v", redacted)
	}
	inlinePrev := &backup.Job{TargetType: "smb", TargetConfig: map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "old", "path": "backups"}}

	tests := []struct {
		name     string
		targetID int64
		prev     *backup.Job
		mutate   func(map[string]any)
		wantPass string
		wantErr  bool
	}{
		{"saved target referenced", saved.ID, nil, nil, "", false},
		{"redacted copy without a target id", 0, nil, nil, "", true},
		{"typed password", 0, nil, func(m map[string]any) { m["password"] = "new"; delete(m, "password_set") }, "new", false},
		{"inline edit keeps the stored password", 0, inlinePrev, nil, "old", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{}
			for k, v := range redacted {
				cfg[k] = v
			}
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			in := &backup.JobInput{TargetID: tc.targetID, TargetType: "smb", TargetConfig: cfg}
			err := resolveJobTargetSecrets(in, tc.prev)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr || tc.targetID > 0 {
				return
			}
			got := redact.ToMap(in.TargetConfig)
			if got["password"] != tc.wantPass {
				t.Fatalf("password = %v, want %q", got["password"], tc.wantPass)
			}
			if _, ok := got["password_set"]; ok {
				t.Fatalf("redaction marker leaked into stored config: %v", got)
			}
		})
	}
}
