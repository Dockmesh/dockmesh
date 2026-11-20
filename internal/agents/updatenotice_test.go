package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/notices"
)

func TestUpdateResultNoticeSaysWhatRuns(t *testing.T) {
	tests := []struct {
		name    string
		running string
		res     AgentUpdateResult
		sev     notices.Severity // "" = no open notice
		title   string
		body    []string
	}{
		{"unconfirmed rollback", "v0.6.0",
			AgentUpdateResult{Status: "rollback_unconfirmed", From: "v0.6.0", To: "v0.6.1", Step: "health", Reason: "v0.6.1 did not report healthy within 3m0s"},
			notices.SevWarning, "Agent update on m1 to v0.6.1 was rolled back, confirmation pending",
			[]string{"put v0.6.0 back and started it", "asked it to confirm", "v0.6.1 did not report healthy"}},
		{"failed, agent back on the previous version", "0.6.0",
			AgentUpdateResult{Status: "failed", From: "v0.6.0", To: "v0.6.1", Step: "health", Reason: "v0.6.1 did not report healthy within 3m0s; rollback: v0.6.0 did not report healthy after the rollback"},
			notices.SevWarning, "Agent update on m1 to v0.6.1 was rolled back, v0.6.0 runs again",
			[]string{"connected on its previous version v0.6.0", "sudo systemctl start dockmesh-agent-update.service"}},
		{"failed, agent on the new version", "v0.6.1",
			AgentUpdateResult{Status: "failed", From: "v0.6.0", To: "v0.6.1", Reason: "rollback: stop: timeout"},
			notices.SevCritical, "Agent update on m1 to v0.6.1 failed and was not rolled back",
			[]string{"could not restore the previous binary"}},
		{"failed without a from version", "",
			AgentUpdateResult{Status: "failed", To: "v0.6.1", Reason: "rollback: stop: timeout"},
			notices.SevCritical, "Agent update on m1 to v0.6.1 failed and was not rolled back", nil},
		{"rolled back", "v0.6.0",
			AgentUpdateResult{Status: "rolled_back", From: "v0.6.0", To: "v0.6.1", Reason: "health"},
			notices.SevWarning, "Agent update on m1 to v0.6.1 was rolled back", []string{"v0.6.0 runs again"}},
		{"updated", "v0.6.1", AgentUpdateResult{Status: "updated", From: "v0.6.0", To: "v0.6.1"}, "", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService(t)
			n := notices.NewService(svc.db)
			svc.SetNotices(n)
			ctx := context.Background()
			svc.updateResultNotice(ctx, "a1", "m1", tt.running, tt.res)
			open, err := n.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got []notices.Notice
			for _, o := range open {
				if o.Key == updateResultNoticePrefix+"a1" {
					got = append(got, o)
				}
			}
			if tt.sev == "" {
				if len(got) != 0 {
					t.Fatalf("open notice %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Severity != tt.sev || got[0].Title != tt.title {
				t.Fatalf("notices %+v, want %s %q", got, tt.sev, tt.title)
			}
			for _, s := range tt.body {
				if !strings.Contains(got[0].Body, s) {
					t.Errorf("body %q lacks %q", got[0].Body, s)
				}
			}
		})
	}
}

// A confirmed rollback replaces the unconfirmed notice under the same key.
func TestUpdateResultNoticeConfirmedReplacesUnconfirmed(t *testing.T) {
	svc, _ := newTestService(t)
	n := notices.NewService(svc.db)
	svc.SetNotices(n)
	ctx := context.Background()
	res := AgentUpdateResult{Status: "rollback_unconfirmed", From: "v0.6.0", To: "v0.6.1", Reason: "health"}
	svc.updateResultNotice(ctx, "a1", "m1", "v0.6.0", res)
	res.Status = "rolled_back"
	svc.updateResultNotice(ctx, "a1", "m1", "v0.6.0", res)
	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, o := range open {
		if o.Key == updateResultNoticePrefix+"a1" {
			titles = append(titles, o.Title)
		}
	}
	if len(titles) != 1 || titles[0] != "Agent update on m1 to v0.6.1 was rolled back" {
		t.Fatalf("open notices %q", titles)
	}
}

// Every connect reports the result again, possibly with another reason;
// that leaves the open notice as it is instead of reopening it, which
// would clear dismissals.
func TestUpdateResultNoticeRepeatedLeavesItAlone(t *testing.T) {
	svc, _ := newTestService(t)
	n := notices.NewService(svc.db)
	svc.SetNotices(n)
	ctx := context.Background()
	res := AgentUpdateResult{Status: "rolled_back", From: "v0.6.0", To: "v0.6.1", Reason: "health"}
	svc.updateResultNotice(ctx, "a1", "m1", "v0.6.0", res)
	open, err := n.Open(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open %+v, %v", open, err)
	}
	// A reopen would stamp created_at again.
	if _, err := svc.db.ExecContext(ctx, `UPDATE notices SET created_at = '2026-01-01 00:00:00' WHERE id = ?`, open[0].ID); err != nil {
		t.Fatal(err)
	}
	if open, err = n.Open(ctx); err != nil {
		t.Fatal(err)
	}
	res.Reason = "another free-text reason"
	svc.updateResultNotice(ctx, "a1", "m1", "v0.6.0", res)
	again, err := n.Open(ctx)
	if err != nil || len(again) != 1 || again[0].ID != open[0].ID || !again[0].CreatedAt.Equal(open[0].CreatedAt) {
		t.Fatalf("repeated result changed the notice: %+v -> %+v (%v)", open, again, err)
	}
}
