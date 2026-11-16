package audit

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Limits for auth.login_failed entries. Failed sign-ins need no
// credentials, so without them anyone could grow the log and flood the
// webhook, for example by looping on a locked account.
const (
	LoginFailureWindow = time.Minute
	LoginFailureBurst  = 10
	maxLoginKeys       = 4096
)

// Reasons a sign-in failed, as counted in a summary entry.
const (
	LoginReasonInvalid = "invalid"
	LoginReasonLocked  = "locked"
	LoginReasonMFA     = "mfa"
)

// LoginFailure is one failed sign-in. IP and Account pick the budgets
// the attempt draws from: IP is the client's rate-limit key, Account the
// normalised username, whether or not it exists. A step that doesn't
// know the account leaves it empty and shares one budget per Reason.
type LoginFailure struct {
	IP      string
	Account string
	Reason  string
}

// loginTally is what the summary of one IP's or one account's window
// reports; others counts the accounts tried from an IP, or the IPs that
// tried an account.
type loginTally struct {
	reasons map[string]int
	others  map[string]int
}

type logins struct {
	budget[loginTally]
}

func newLogins() logins {
	return logins{budget[loginTally]{window: LoginFailureWindow, burst: LoginFailureBurst, maxKeys: maxLoginKeys}}
}

const (
	loginKeyIP      = "ip:"
	loginKeyAccount = "account:"
	loginKeyStep    = "step:"
)

// WriteLoginFailed records an auth.login_failed entry while both the IP
// and the account of f had fewer than LoginFailureBurst entries in their
// current windows. Otherwise the attempt is counted in the summary of
// one of them: the IP's if that is full, else the account's. Entries are
// written even if the request is cancelled meanwhile.
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

// StartLoginFailureFlusher writes finished login summaries every
// LoginFailureWindow. The returned stop writes the open windows and
// returns once they are stored; call it before the webhook and the
// database are closed.
func (s *Service) StartLoginFailureFlusher(ctx context.Context) (stop func()) {
	return startFlusher(ctx, LoginFailureWindow, s.flushLogins)
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
			details["usernames"] = b.tally.others
		} else {
			if account, ok := strings.CutPrefix(b.key, loginKeyAccount); ok {
				details["username"] = account
			} else {
				details["step"] = strings.TrimPrefix(b.key, loginKeyStep)
			}
			details["ips"] = b.tally.others
		}
		s.Write(ctx, "", ActionLoginFailed, fmt.Sprintf("%d more failed sign-ins", b.suppressed), details)
	}
}

func (l *logins) admit(f LoginFailure) (bool, []bucket[loginTally]) {
	ipKey, accountKey := loginKeyIP+f.IP, loginKeyAccount+f.Account
	if f.Account == "" {
		accountKey = loginKeyStep + f.Reason
	}
	return l.charge([]string{ipKey, accountKey}, func(key string, t *loginTally) {
		t.reasons = countCapped(t.reasons, f.Reason)
		switch {
		case key == accountKey:
			t.others = countCapped(t.others, f.IP)
		case f.Account != "":
			t.others = countCapped(t.others, f.Account)
		}
	})
}
