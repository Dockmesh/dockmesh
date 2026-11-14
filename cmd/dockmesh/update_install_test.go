package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

func TestAuditUpdateResult(t *testing.T) {
	tests := []struct {
		status rootupdate.Status
		action string
	}{
		{rootupdate.StatusUpdated, audit.ActionUpdateApplied},
		{rootupdate.StatusRefused, audit.ActionUpdateRefused},
		{rootupdate.StatusRolledBack, audit.ActionUpdateRolledBack},
		{rootupdate.StatusFailed, audit.ActionUpdateFailed},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			ctx := context.Background()
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			a := audit.NewService(database, filepath.Join(t.TempDir(), "genesis"))
			if err := auditUpdateResult(ctx, a, rootupdate.Result{Status: tt.status, From: "v0.6.0", To: "v0.6.1", Step: "health", Reason: "why"}); err != nil {
				t.Fatal(err)
			}
			entries, err := a.ListFiltered(ctx, audit.ListFilter{Action: tt.action})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("%d %s entries, want 1", len(entries), tt.action)
			}
			e := entries[0]
			if e.UserID != "" || e.Target != "v0.6.1" || e.Category != audit.CategorySystem {
				t.Fatalf("entry %+v, want a system entry for v0.6.1", e)
			}
			var d map[string]string
			if err := json.Unmarshal([]byte(e.Details), &d); err != nil {
				t.Fatal(err)
			}
			if d["from"] != "v0.6.0" || d["to"] != "v0.6.1" || d["step"] != "health" || d["reason"] != "why" {
				t.Fatalf("details %v", d)
			}
		})
	}
}
