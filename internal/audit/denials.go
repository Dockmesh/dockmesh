package audit

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Limits for auth.scope_denied entries, so a caller looping a refused
// request can't grow the log without bound or push other events out of
// the webhook queue.
const (
	DenialWindow      = time.Minute
	DenialBurst       = 10
	maxDenialActors   = 4096
	maxDenialTokenIDs = maxSummaryNames
)

// DenialActor identifies who a refusal is counted for. Key separates the
// budgets; a user's sessions and API tokens share one, so minting tokens
// doesn't buy more entries. TokenID is the token of this request, if any.
type DenialActor struct {
	Key     string
	UserID  string
	TokenID int64
}

// denialTally is what the summary of an actor's window reports.
type denialTally struct {
	actor  DenialActor
	perms  map[string]int
	tokens map[string]int
}

type denials struct {
	budget[denialTally]
}

func newDenials() denials {
	return denials{budget[denialTally]{window: DenialWindow, burst: DenialBurst, maxKeys: maxDenialActors}}
}

// WriteDenied records an auth.scope_denied entry for actor, unless actor
// already had DenialBurst entries in the current window; then the refusal
// is counted for the window's summary entry, which names the refused
// permissions. Entries are written even if the request is cancelled
// meanwhile, so a dropped connection can't discard a window's count.
func (s *Service) WriteDenied(ctx context.Context, actor DenialActor, perm, target string, details any) {
	ctx = context.WithoutCancel(ctx)
	write, summaries := s.denials.admit(actor, perm)
	s.writeDenialSummaries(ctx, summaries)
	if write {
		s.Write(ctx, actor.UserID, ActionScopeDenied, target, details)
	}
}

// flushDenials writes the summary entries of every window that is over,
// or of every open window when all is set.
func (s *Service) flushDenials(ctx context.Context, all bool) {
	s.writeDenialSummaries(ctx, s.denials.expired(all))
}

// StartDenialFlusher writes finished summaries every DenialWindow, so the
// count of a burst is logged even when no further refusal follows. The
// returned stop writes the open windows and returns once they are
// stored; call it before the webhook and the database are closed.
func (s *Service) StartDenialFlusher(ctx context.Context) (stop func()) {
	return startFlusher(ctx, DenialWindow, s.flushDenials)
}

func (s *Service) writeDenialSummaries(ctx context.Context, summaries []bucket[denialTally]) {
	for _, b := range summaries {
		details := map[string]any{
			"suppressed":   b.suppressed,
			"window_start": b.start.UTC().Format(time.RFC3339),
			"window_end":   b.end.UTC().Format(time.RFC3339),
			"permissions":  b.tally.perms,
		}
		if len(b.tally.tokens) > 0 {
			details["api_tokens"] = b.tally.tokens
		}
		actor := b.tally.actor
		if actor.UserID == "" {
			details["actor"] = actor.Key
		}
		s.Write(ctx, actor.UserID, ActionScopeDenied, fmt.Sprintf("%d more refused requests", b.suppressed), details)
	}
}

// admit reports whether this refusal is written in full, and returns the
// windows that ended before it with refusals still to summarise.
func (d *denials) admit(actor DenialActor, perm string) (bool, []bucket[denialTally]) {
	return d.charge([]string{actor.Key}, func(_ string, t *denialTally) {
		if t.actor.Key == "" {
			t.actor = actor
		}
		t.perms = countCapped(t.perms, perm)
		if actor.TokenID != 0 {
			t.tokens = countCapped(t.tokens, strconv.FormatInt(actor.TokenID, 10))
		}
	})
}
