package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newLoginService(t *testing.T) (*Service, *countingWebhook, *time.Time) {
	t.Helper()
	s, hook, now := newDenialService(t)
	s.logins.now = func() time.Time { return *now }
	return s, hook, now
}

// loginFailedEntries returns every auth.login_failed row, newest first;
// List caps its page size below what the flood test writes.
func loginFailedEntries(t *testing.T, s *Service) []Entry {
	t.Helper()
	rows, err := s.db.Query(`SELECT target, details FROM audit_log WHERE action = ? ORDER BY id DESC`, ActionLoginFailed)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var target, details sql.NullString
		if err := rows.Scan(&target, &details); err != nil {
			t.Fatal(err)
		}
		out = append(out, Entry{Action: ActionLoginFailed, Target: target.String, Details: details.String})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type loginSummary struct {
	Suppressed int            `json:"suppressed"`
	IP         string         `json:"ip"`
	Username   string         `json:"username"`
	Scope      string         `json:"scope"`
	Reasons    map[string]int `json:"reasons"`
	Usernames  map[string]int `json:"usernames"`
	IPs        map[string]int `json:"ips"`
}

// splitLoginEntries returns the number of single attempts and the
// summaries among entries.
func splitLoginEntries(t *testing.T, entries []Entry) (int, []loginSummary) {
	t.Helper()
	full := 0
	var sums []loginSummary
	for _, e := range entries {
		if !strings.HasSuffix(e.Target, " more failed sign-ins") {
			full++
			continue
		}
		var d loginSummary
		if err := json.Unmarshal([]byte(e.Details), &d); err != nil {
			t.Fatal(err)
		}
		if e.Target != fmt.Sprintf("%d more failed sign-ins", d.Suppressed) {
			t.Fatalf("summary target %q, details %s", e.Target, e.Details)
		}
		sums = append(sums, d)
	}
	return full, sums
}

func TestWriteLoginFailedFlood(t *testing.T) {
	const attempts = 10000
	tests := []struct {
		name string
		step time.Duration // clock advance per attempt
	}{
		{name: "one burst", step: 0},
		{name: "paced over hours", step: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, hook, now := newLoginService(t)
			start := *now
			lastFlush := *now
			for i := range attempts {
				*now = now.Add(tt.step)
				if now.Sub(lastFlush) >= loginFlushInterval {
					s.flushLogins(ctx, false)
					lastFlush = *now
				}
				var f LoginFailure
				switch i % 5 {
				case 0:
					f = LoginFailure{IP: "203.0.113.9", Account: "admin", Reason: LoginReasonLocked}
				case 1:
					f = LoginFailure{IP: "203.0.113.9", Account: fmt.Sprintf("ghost%d", i), Reason: LoginReasonInvalid}
				case 2:
					f = LoginFailure{IP: fmt.Sprintf("198.51.100.%d", i%250), Account: "admin", Reason: LoginReasonLocked}
				case 3:
					// A fresh address and username every time: only the
					// global budget holds these.
					f = LoginFailure{IP: fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), Account: fmt.Sprintf("fresh%d", i), Reason: LoginReasonInvalid}
				case 4:
					f = LoginFailure{IP: fmt.Sprintf("2001:db8:%x::", i), Reason: LoginReasonMFAToken}
				}
				s.WriteLoginFailed(ctx, f, f.Account, map[string]string{"ip": f.IP})
			}
			// Each single entry creates at most two keys besides the global
			// one, and the flusher forgets a key one window after it was
			// created.
			if n := len(s.logins.buckets); n > 2*2*LoginFailureGlobalBurst+1 {
				t.Fatalf("tracked keys = %d", n)
			}
			s.flushLogins(ctx, true)

			entries := loginFailedEntries(t, s)
			full, sums := splitLoginEntries(t, entries)
			suppressed := 0
			for _, d := range sums {
				n := 0
				for _, c := range d.Reasons {
					n += c
				}
				if n != d.Suppressed {
					t.Fatalf("reasons add up to %d, suppressed %d: %+v", n, d.Suppressed, d)
				}
				suppressed += d.Suppressed
			}
			if full+suppressed != attempts {
				t.Fatalf("%d entries + %d summarised = %d, want %d", full, suppressed, full+suppressed, attempts)
			}
			// Per global window at most LoginFailureGlobalBurst single
			// entries; an IP or username summary needs LoginFailureBurst of
			// them first, and there is one global summary. No matter how
			// many addresses and names are used.
			windows := int(now.Sub(start)/LoginFailureWindow) + 1
			bound := windows * (LoginFailureGlobalBurst + 2*LoginFailureGlobalBurst/LoginFailureBurst + 1)
			t.Logf("%d attempts over %d windows: %d audit rows (%d single, %d summaries), bound %d",
				attempts, windows, len(entries), full, len(sums), bound)
			if len(entries) > bound {
				t.Fatalf("audit rows = %d, want at most %d", len(entries), bound)
			}
			if got := len(hook.entries); got != len(entries) {
				t.Fatalf("webhook deliveries = %d, audit rows = %d", got, len(entries))
			}
			if len(s.denials.buckets) != 0 {
				t.Fatalf("login failures used the refusal budget: %d buckets", len(s.denials.buckets))
			}
			rep, err := s.Verify(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Broken != 0 {
				t.Fatalf("chain broken: %+v", rep)
			}
		})
	}
}

func TestWriteLoginFailedBudgets(t *testing.T) {
	tests := []struct {
		name     string
		burst    int
		failures func(i int) LoginFailure
		want     loginSummary
	}{
		{
			name:  "one IP, many usernames",
			burst: LoginFailureBurst,
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: "192.0.2.1", Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid}
			},
			want: loginSummary{Suppressed: 15, IP: "192.0.2.1", Reasons: map[string]int{LoginReasonInvalid: 15}},
		},
		{
			name:  "one locked username, many IPs",
			burst: LoginFailureBurst,
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: fmt.Sprintf("192.0.2.%d", i), Account: "admin", Reason: LoginReasonLocked}
			},
			want: loginSummary{Suppressed: 15, Username: "admin", Reasons: map[string]int{LoginReasonLocked: 15}},
		},
		{
			name:  "many IPs, fresh usernames",
			burst: LoginFailureGlobalBurst,
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: fmt.Sprintf("10.0.%d.%d", i/250, i%250), Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid}
			},
			want: loginSummary{Suppressed: 15, Scope: loginKeyGlobal, Reasons: map[string]int{LoginReasonInvalid: 15}},
		},
		{
			name:  "forged MFA tokens from many IPs",
			burst: LoginFailureGlobalBurst,
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: fmt.Sprintf("10.0.%d.%d", i/250, i%250), Reason: LoginReasonMFAToken}
			},
			want: loginSummary{Suppressed: 15, Scope: loginKeyGlobal, Reasons: map[string]int{LoginReasonMFAToken: 15}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, _, now := newLoginService(t)
			for i := range tt.burst + tt.want.Suppressed {
				s.WriteLoginFailed(ctx, tt.failures(i), "x", nil)
			}
			if got := len(loginFailedEntries(t, s)); got != tt.burst {
				t.Fatalf("entries during the burst = %d, want %d", got, tt.burst)
			}
			*now = now.Add(LoginFailureWindow)
			s.flushLogins(ctx, false)
			full, sums := splitLoginEntries(t, loginFailedEntries(t, s))
			if full != tt.burst || len(sums) != 1 {
				t.Fatalf("entries = %d, summaries = %+v", full, sums)
			}
			got := sums[0]
			if got.Suppressed != tt.want.Suppressed || got.IP != tt.want.IP || got.Username != tt.want.Username ||
				got.Scope != tt.want.Scope || fmt.Sprint(got.Reasons) != fmt.Sprint(tt.want.Reasons) {
				t.Fatalf("summary = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A username that ran out of room stays named in the summary of the
// address it was tried from, past the cap on distinct names.
func TestWriteLoginFailedKeepsFullAccountsNamed(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newLoginService(t)
	const ip = "192.0.2.1"
	for range LoginFailureBurst {
		s.WriteLoginFailed(ctx, LoginFailure{IP: ip, Account: "admin", Reason: LoginReasonLocked}, "admin", nil)
	}
	const fresh = 2 * maxSummaryNames
	for i := range fresh {
		s.WriteLoginFailed(ctx, LoginFailure{IP: ip, Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid}, "x", nil)
	}
	s.WriteLoginFailed(ctx, LoginFailure{IP: ip, Account: "admin", Reason: LoginReasonLocked}, "admin", nil)
	s.flushLogins(ctx, true)
	_, sums := splitLoginEntries(t, loginFailedEntries(t, s))
	if len(sums) != 1 {
		t.Fatalf("summaries = %+v", sums)
	}
	d := sums[0]
	total := 0
	for _, c := range d.Usernames {
		total += c
	}
	if d.IP != ip || d.Suppressed != fresh+1 || d.Usernames["admin"] != 1 || d.Usernames["other"] == 0 || total != d.Suppressed {
		t.Fatalf("summary = %+v", d)
	}
}

// A cancelled request still stores its attempt and the summary it
// triggers.
func TestWriteLoginFailedCancelledRequest(t *testing.T) {
	s, _, now := newLoginService(t)
	f := LoginFailure{IP: "192.0.2.1", Account: "admin", Reason: LoginReasonLocked}
	for range LoginFailureBurst + 4 {
		s.WriteLoginFailed(context.Background(), f, "admin", nil)
	}
	*now = now.Add(LoginFailureWindow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.WriteLoginFailed(ctx, f, "admin", nil)
	entries := loginFailedEntries(t, s)
	if len(entries) != LoginFailureBurst+2 || entries[1].Target != "4 more failed sign-ins" || entries[0].Target != "admin" {
		t.Fatalf("entries = %d: %+v", len(entries), entries[:2])
	}
}

// Stopping the flusher writes the windows that are still open.
func TestLoginFailureFlusherStopWritesOpenWindows(t *testing.T) {
	ctx := context.Background()
	s, hook, _ := newLoginService(t)
	stop := s.StartLoginFailureFlusher(ctx)
	for i := range LoginFailureBurst + 3 {
		s.WriteLoginFailed(ctx, LoginFailure{IP: "192.0.2.1", Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid}, "x", nil)
	}
	stop()
	stop()
	entries := loginFailedEntries(t, s)
	if len(entries) != LoginFailureBurst+1 || entries[0].Target != "3 more failed sign-ins" {
		t.Fatalf("entries = %d, newest %+v", len(entries), entries[0])
	}
	_, sums := splitLoginEntries(t, entries[:1])
	if d := sums[0]; d.IP != "192.0.2.1" || len(d.Usernames) != 3 || d.Usernames["u12"] != 1 {
		t.Fatalf("summary = %+v", d)
	}
	if len(hook.entries) != len(entries) {
		t.Fatalf("webhook deliveries = %d, audit rows = %d", len(hook.entries), len(entries))
	}
}

// When every slot holds a live window, the oldest is ended early and its
// summary returned, so no count is lost.
func TestBudgetKeyCap(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	b := budget[int]{window: time.Minute, maxKeys: 3, now: func() time.Time { return now }}
	count := func(_ string, n *int, _ func(string) bool) { *n++ }
	b.charge([]budgetKey{{"a", 1}}, count)
	b.charge([]budgetKey{{"a", 1}}, count)
	for _, k := range []string{"b", "c"} {
		now = now.Add(time.Millisecond)
		b.charge([]budgetKey{{k, 1}}, count)
	}
	now = now.Add(time.Millisecond)
	write, out := b.charge([]budgetKey{{"d", 1}}, count)
	if !write || len(b.buckets) != 3 || b.buckets["a"] != nil {
		t.Fatalf("write = %v, keys = %v", write, b.buckets)
	}
	if len(out) != 1 || out[0].key != "a" || out[0].suppressed != 1 || out[0].tally != 1 || !out[0].end.Equal(now) {
		t.Fatalf("evicted = %+v", out)
	}
}
