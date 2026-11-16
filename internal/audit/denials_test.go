package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

type countingWebhook struct {
	mu      sync.Mutex
	entries []WebhookEntry
}

func (c *countingWebhook) Dispatch(e WebhookEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func newDenialService(t *testing.T) (*Service, *countingWebhook, *time.Time) {
	t.Helper()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := NewService(database, filepath.Join(t.TempDir(), "genesis"))
	if err := s.EnsureGenesis(context.Background()); err != nil {
		t.Fatal(err)
	}
	hook := &countingWebhook{}
	s.SetWebhook(hook)
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s.denials.now = func() time.Time { return now }
	return s, hook, &now
}

func deniedEntries(t *testing.T, s *Service) []Entry {
	t.Helper()
	entries, err := s.ListFiltered(context.Background(), ListFilter{Action: ActionScopeDenied, Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestWriteDeniedBurst(t *testing.T) {
	ctx := context.Background()
	s, hook, now := newDenialService(t)

	for i := 0; i < 10000; i++ {
		perm := "users.view"
		if i%2 == 1 {
			perm = "audit.view"
		}
		s.WriteDenied(ctx, "u1", "u1", perm, "GET /api/v1/users", map[string]any{"permission": perm})
	}
	if got := len(deniedEntries(t, s)); got != DenialBurst {
		t.Fatalf("entries during the burst = %d, want %d", got, DenialBurst)
	}

	// Another actor keeps its own budget.
	s.WriteDenied(ctx, "u2", "u2", "users.view", "GET /api/v1/users", nil)
	if got := len(deniedEntries(t, s)); got != DenialBurst+1 {
		t.Fatalf("entries after second actor = %d", got)
	}

	s.FlushDenials(ctx)
	if got := len(deniedEntries(t, s)); got != DenialBurst+1 {
		t.Fatalf("flush inside the window wrote a summary: %d entries", got)
	}

	*now = now.Add(DenialWindow)
	s.FlushDenials(ctx)
	entries := deniedEntries(t, s)
	if len(entries) != DenialBurst+2 {
		t.Fatalf("entries after flush = %d, want %d", len(entries), DenialBurst+2)
	}
	sum := entries[0]
	if sum.UserID != "u1" || sum.Target != "9990 more refused requests" {
		t.Fatalf("summary = %+v", sum)
	}
	var d struct {
		Suppressed  int            `json:"suppressed"`
		Permissions map[string]int `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(sum.Details), &d); err != nil {
		t.Fatal(err)
	}
	if d.Suppressed != 9990 || d.Permissions["users.view"] != 4995 || d.Permissions["audit.view"] != 4995 {
		t.Fatalf("summary details = %s", sum.Details)
	}

	// A new window starts with a fresh budget.
	s.WriteDenied(ctx, "u1", "u1", "users.view", "GET /api/v1/users", nil)
	if got := len(deniedEntries(t, s)); got != DenialBurst+3 {
		t.Fatalf("entries in the next window = %d", got)
	}

	if got := len(hook.entries); got != DenialBurst+3 {
		t.Fatalf("webhook deliveries = %d, want %d", got, DenialBurst+3)
	}
	rep, err := s.Verify(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Broken != 0 {
		t.Fatalf("chain broken: %+v", rep)
	}
}

// The summary of a finished window is written by the next refusal of the
// same actor too, not only by the flusher.
func TestWriteDeniedSummaryOnNextWindow(t *testing.T) {
	ctx := context.Background()
	s, _, now := newDenialService(t)
	for i := 0; i < DenialBurst+5; i++ {
		s.WriteDenied(ctx, "ip:192.0.2.1", "", "users.view", "GET /x", nil)
	}
	*now = now.Add(DenialWindow + time.Second)
	s.WriteDenied(ctx, "ip:192.0.2.1", "", "users.view", "GET /x", nil)
	entries := deniedEntries(t, s)
	if len(entries) != DenialBurst+2 {
		t.Fatalf("entries = %d", len(entries))
	}
	// Newest first: the refusal of the new window, then the summary.
	if entries[1].Target != "5 more refused requests" {
		t.Fatalf("summary = %+v", entries[1])
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(entries[1].Details), &d); err != nil {
		t.Fatal(err)
	}
	if d["actor"] != "ip:192.0.2.1" {
		t.Fatalf("summary details = %s", entries[1].Details)
	}
}

// Distinct actors can't grow the tracker without bound: once every slot
// holds a live window, further actors share one bucket.
func TestWriteDeniedActorCap(t *testing.T) {
	ctx := context.Background()
	s, _, now := newDenialService(t)
	for i := 0; i < maxDenialActors; i++ {
		s.denials.admit(fmt.Sprintf("ip:a%d", i), "", "users.view")
	}
	for i := 0; i < 1000; i++ {
		s.WriteDenied(ctx, fmt.Sprintf("ip:b%d", i), "", "users.view", "GET /x", nil)
	}
	if got := len(deniedEntries(t, s)); got != DenialBurst {
		t.Fatalf("entries from new actors = %d, want %d", got, DenialBurst)
	}
	if n := len(s.denials.buckets); n > maxDenialActors+1 {
		t.Fatalf("tracked actors = %d", n)
	}
	*now = now.Add(DenialWindow)
	s.FlushDenials(ctx)
	entries := deniedEntries(t, s)
	if entries[0].Target != fmt.Sprintf("%d more refused requests", 1000-DenialBurst) {
		t.Fatalf("overflow summary = %+v", entries[0])
	}
	if n := len(s.denials.buckets); n != 0 {
		t.Fatalf("buckets left after flush = %d", n)
	}
}
