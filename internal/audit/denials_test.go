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
		s.WriteDenied(ctx, DenialActor{Key: "u1", UserID: "u1"}, perm, "GET /api/v1/users", map[string]any{"permission": perm})
	}
	if got := len(deniedEntries(t, s)); got != DenialBurst {
		t.Fatalf("entries during the burst = %d, want %d", got, DenialBurst)
	}

	// Another actor keeps its own budget.
	s.WriteDenied(ctx, DenialActor{Key: "u2", UserID: "u2"}, "users.view", "GET /api/v1/users", nil)
	if got := len(deniedEntries(t, s)); got != DenialBurst+1 {
		t.Fatalf("entries after second actor = %d", got)
	}

	s.flushDenials(ctx, false)
	if got := len(deniedEntries(t, s)); got != DenialBurst+1 {
		t.Fatalf("flush inside the window wrote a summary: %d entries", got)
	}

	*now = now.Add(DenialWindow)
	s.flushDenials(ctx, false)
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
	s.WriteDenied(ctx, DenialActor{Key: "u1", UserID: "u1"}, "users.view", "GET /api/v1/users", nil)
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
		s.WriteDenied(ctx, DenialActor{Key: "ip:192.0.2.1", UserID: ""}, "users.view", "GET /x", nil)
	}
	*now = now.Add(DenialWindow + time.Second)
	s.WriteDenied(ctx, DenialActor{Key: "ip:192.0.2.1", UserID: ""}, "users.view", "GET /x", nil)
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

// Distinct actors can't grow the tracker without bound: when every slot
// holds a live window, the oldest is ended early and its summary written.
func TestWriteDeniedActorCap(t *testing.T) {
	ctx := context.Background()
	s, _, now := newDenialService(t)
	for i := 0; i < maxDenialActors; i++ {
		actor := DenialActor{Key: fmt.Sprintf("user:a%d", i), UserID: fmt.Sprintf("a%d", i)}
		for j := 0; j <= DenialBurst; j++ {
			s.denials.admit(actor, "users.view")
		}
		*now = now.Add(time.Millisecond)
	}
	s.WriteDenied(ctx, DenialActor{Key: "user:b", UserID: "b"}, "users.view", "GET /x", nil)
	if n := len(s.denials.buckets); n > maxDenialActors {
		t.Fatalf("tracked actors = %d", n)
	}
	entries := deniedEntries(t, s)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want the evicted summary and the new refusal", len(entries))
	}
	if entries[1].UserID != "a0" || entries[1].Target != "1 more refused requests" {
		t.Fatalf("evicted summary = %+v", entries[1])
	}
	if entries[0].UserID != "b" {
		t.Fatalf("new refusal = %+v", entries[0])
	}
}

// Stopping the flusher writes the windows that are still open.
func TestDenialFlusherStopWritesOpenWindows(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newDenialService(t)
	stop := s.StartDenialFlusher(ctx)
	actor := DenialActor{Key: "user:u1", UserID: "u1", TokenID: 7}
	for i := 0; i < DenialBurst+3; i++ {
		s.WriteDenied(ctx, actor, "users.view", "GET /x", nil)
	}
	stop()
	stop()
	entries := deniedEntries(t, s)
	if len(entries) != DenialBurst+1 || entries[0].Target != "3 more refused requests" {
		t.Fatalf("entries = %d, newest %+v", len(entries), entries[0])
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(entries[0].Details), &d); err != nil {
		t.Fatal(err)
	}
	if tokens, _ := d["api_tokens"].(map[string]any); tokens["7"] != float64(3) {
		t.Fatalf("summary details = %s", entries[0].Details)
	}
}

// A cancelled request still stores the summary its refusal triggers.
func TestWriteDeniedCancelledRequestKeepsSummary(t *testing.T) {
	s, _, now := newDenialService(t)
	actor := DenialActor{Key: "user:u1", UserID: "u1"}
	for i := 0; i < DenialBurst+4; i++ {
		s.WriteDenied(context.Background(), actor, "users.view", "GET /x", nil)
	}
	*now = now.Add(DenialWindow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.WriteDenied(ctx, actor, "users.view", "GET /x", nil)
	entries := deniedEntries(t, s)
	if len(entries) != DenialBurst+2 || entries[1].Target != "4 more refused requests" {
		t.Fatalf("entries = %d: %+v", len(entries), entries[:2])
	}
}

// Minting tokens gives no extra budget: every token of a user draws from
// the user's one.
func TestWriteDeniedTokenRotation(t *testing.T) {
	ctx := context.Background()
	s, _, now := newDenialService(t)
	for i := 1; i <= maxDenialActors+200; i++ {
		s.WriteDenied(ctx, DenialActor{Key: "user:u1", UserID: "u1", TokenID: int64(i)}, "users.view", "GET /x", nil)
	}
	if got := len(deniedEntries(t, s)); got != DenialBurst {
		t.Fatalf("entries = %d, want %d", got, DenialBurst)
	}
	*now = now.Add(DenialWindow)
	s.flushDenials(ctx, false)
	entries := deniedEntries(t, s)
	if len(entries) != DenialBurst+1 {
		t.Fatalf("entries after flush = %d", len(entries))
	}
	var d struct {
		Tokens map[string]int `json:"api_tokens"`
	}
	if err := json.Unmarshal([]byte(entries[0].Details), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Tokens) != maxSummaryNames+1 || d.Tokens["other"] == 0 {
		t.Fatalf("token breakdown has %d keys: %v", len(d.Tokens), d.Tokens)
	}
}
