package audit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Limits for auth.login_failed entries. Failed sign-ins need no
// credentials, so without them anyone could grow the log and flood the
// webhook, for example by looping on a locked account. The window is as
// long as the per-IP sign-in limit's, so a client pacing itself below
// that limit still runs out of entries; the global budget does the same
// for a client spread over many addresses.
const (
	LoginFailureWindow      = 15 * time.Minute
	LoginFailureBurst       = 10
	LoginFailureGlobalBurst = 300
	loginFlushInterval      = time.Minute
	maxLoginKeys            = 4096
)

// Reasons a sign-in failed, as counted in a summary entry.
const (
	LoginReasonInvalid  = "invalid"
	LoginReasonLocked   = "locked"
	LoginReasonMFA      = "mfa"
	LoginReasonMFAToken = "mfa_token"
)

// LoginFailure is one failed sign-in. IP and Account pick the budgets
// the attempt draws from besides the global one: IP is the client's
// rate-limit key, Account the normalised username, whether or not it
// exists. An attempt that names no account leaves it empty.
type LoginFailure struct {
	IP      string
	Account string
	Reason  string
}

// loginTally is what the summary of one window reports.
type loginTally struct {
	reasons   map[string]int
	ips       map[string]int
	usernames map[string]int
}

type logins struct {
	budget[loginTally]
}

func newLogins() logins {
	return logins{budget[loginTally]{window: LoginFailureWindow, maxKeys: maxLoginKeys}}
}

const (
	loginKeyIP      = "ip:"
	loginKeyAccount = "account:"
	loginKeyGlobal  = "global"
)

// WriteLoginFailed records an auth.login_failed entry while the IP, the
// account and the global budget all have room in their current windows.
// Otherwise the attempt is counted in the summary of the first that is
// full, in that order. Entries are written even if the request is
// cancelled meanwhile.
func (s *Service) WriteLoginFailed(ctx context.Context, f LoginFailure, target string, details any) {
	ctx = context.WithoutCancel(ctx)
	write, summaries := s.logins.admit(f)
	s.writeLoginSummaries(ctx, summaries)
	if write {
		s.Write(ctx, "", ActionLoginFailed, target, details)
	}
}

func (s *Service) flushLogins(ctx context.Context, all bool) {
	s.writeLoginSummaries(ctx, s.logins.expired(all))
}

// StartLoginFailureFlusher writes finished login summaries every minute.
// The returned stop writes the open windows and returns once they are
// stored; call it before the webhook and the database are closed.
func (s *Service) StartLoginFailureFlusher(ctx context.Context) (stop func()) {
	return startFlusher(ctx, loginFlushInterval, s.flushLogins)
}

func (s *Service) writeLoginSummaries(ctx context.Context, summaries []bucket[loginTally]) {
	for _, b := range summaries {
		details := map[string]any{
			"suppressed":   b.suppressed,
			"window_start": b.start.UTC().Format(time.RFC3339),
			"window_end":   b.end.UTC().Format(time.RFC3339),
			"reasons":      b.tally.reasons,
		}
		if ip, ok := strings.CutPrefix(b.key, loginKeyIP); ok {
			details["ip"] = ip
		} else if account, ok := strings.CutPrefix(b.key, loginKeyAccount); ok {
			details["username"] = account
		} else {
			details["scope"] = loginKeyGlobal
		}
		if len(b.tally.ips) > 0 {
			details["ips"] = b.tally.ips
		}
		if len(b.tally.usernames) > 0 {
			details["usernames"] = b.tally.usernames
		}
		s.writeSummary(ctx, "", ActionLoginFailed, fmt.Sprintf("%d more failed sign-ins", b.suppressed), details)
	}
}

func (l *logins) admit(f LoginFailure) (bool, []bucket[loginTally]) {
	ipKey := loginKeyIP + f.IP
	accountKey := ""
	keys := []budgetKey{{ipKey, LoginFailureBurst}}
	if f.Account != "" {
		accountKey = loginKeyAccount + f.Account
		keys = append(keys, budgetKey{accountKey, LoginFailureBurst})
	}
	keys = append(keys, budgetKey{loginKeyGlobal, LoginFailureGlobalBurst})
	return l.charge(keys, func(key string, t *loginTally, full func(string) bool) {
		t.reasons = countCapped(t.reasons, f.Reason, false)
		// An address or account that ran out of room is named even past
		// the cap, so a targeted one doesn't disappear into "other".
		if key != ipKey {
			t.ips = countCapped(t.ips, f.IP, full(ipKey))
		}
		if accountKey != "" && key != accountKey {
			t.usernames = countCapped(t.usernames, f.Account, full(accountKey))
		}
	})
}
