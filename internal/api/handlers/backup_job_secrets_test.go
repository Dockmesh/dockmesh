package handlers

import (
	"context"

	"testing"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/redact"
)

func TestJobFromRedactedTargetKeepsPassword(t *testing.T) {
	ctx := context.Background()
	store := targets.NewTargetStore(openRedactionTestDB(t))
	saved, err := store.Create(ctx, targets.TargetInput{
		Name: "nas", Type: "smb",
		Config: map[string]any{"host": "nas", "share": "bk", "username": "u", "password": "s3cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handlers{BackupTargets: store}

	// What the wizard copies from GET /backups/targets.
	redacted := redact.ToMap(redactTarget(saved).Config)
	if redacted["password"] != "" || redacted["password_set"] != true {
		t.Fatalf("target not redacted: %v", redacted)
	}

	tests := []struct {
		name     string
		targetID int64
		mutate   func(map[string]any)
		wantPass string
		wantErr  bool
	}{
		{"picked saved target", saved.ID, nil, "s3cret", false},
		{"typed password wins", saved.ID, func(m map[string]any) { m["password"] = "new" }, "new", false},
		{"no target id, redacted copy", 0, nil, "", true},
		{"other server", saved.ID, func(m map[string]any) { m["host"] = "evil" }, "", true},
		{"unknown target", 999, nil, "", true},
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
			in := &backup.JobInput{TargetType: "smb", TargetConfig: cfg}
			err := h.resolveJobTargetSecrets(ctx, in, tc.targetID, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
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
