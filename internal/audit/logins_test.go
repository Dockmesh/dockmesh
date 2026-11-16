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
	Step       string         `json:"step"`
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
	ctx := context.Background()
	s, hook, now := newLoginService(t)

	const (
		attempts = 10000
		manyIPs  = 100
		windows  = 2
	)
	for i := range attempts {
		if i == attempts/2 {
			*now = now.Add(LoginFailureWindow)
		}
		ip := "203.0.113.9"
		if i%4 >= 2 {
			ip = fmt.Sprintf("198.51.100.%d", i%manyIPs)
		}
		f := LoginFailure{IP: ip, Account: "admin", Reason: LoginReasonLocked}
		switch {
		case i%8 == 7:
			f = LoginFailure{IP: ip, Reason: LoginReasonMFA}
		case i%2 == 1:
			f = LoginFailure{IP: ip, Account: fmt.Sprintf("ghost%d", i), Reason: LoginReasonInvalid}
		}
		s.WriteLoginFailed(ctx, f, f.Account, map[string]string{"ip": ip})
	}
	*now = now.Add(LoginFailureWindow)
	s.flushLogins(ctx, false)

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
	// A single entry takes room from an IP, so per window there are at
	// most LoginFailureBurst entries and one summary per IP, plus the
	// summaries of the two usernames that fill up ("admin" and the
	// shared MFA step); the unique ghost names never fill up.
	ips := 1 + manyIPs
	bound := windows * (ips*(LoginFailureBurst+1) + 2)
	if len(entries) > bound {
		t.Fatalf("audit rows = %d, want at most %d", len(entries), bound)
	}
	t.Logf("%d attempts: %d audit rows (%d single, %d summaries), bound %d", attempts, len(entries), full, len(sums), bound)
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
}

func TestWriteLoginFailedBudgets(t *testing.T) {
	tests := []struct {
		name     string
		failures func(i int) LoginFailure
		want     loginSummary
	}{
		{
			name: "one IP, many usernames",
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: "192.0.2.1", Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid}
			},
			want: loginSummary{Suppressed: 15, IP: "192.0.2.1", Reasons: map[string]int{LoginReasonInvalid: 15}},
		},
		{
			name: "one locked username, many IPs",
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: fmt.Sprintf("192.0.2.%d", i), Account: "admin", Reason: LoginReasonLocked}
			},
			want: loginSummary{Suppressed: 15, Username: "admin", Reasons: map[string]int{LoginReasonLocked: 15}},
		},
		{
			name: "MFA step from many IPs",
			failures: func(i int) LoginFailure {
				return LoginFailure{IP: fmt.Sprintf("192.0.2.%d", i), Reason: LoginReasonMFA}
			},
			want: loginSummary{Suppressed: 15, Step: LoginReasonMFA, Reasons: map[string]int{LoginReasonMFA: 15}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, _, now := newLoginService(t)
			for i := range LoginFailureBurst + tt.want.Suppressed {
				s.WriteLoginFailed(ctx, tt.failures(i), "x", nil)
			}
			if got := len(loginFailedEntries(t, s)); got != LoginFailureBurst {
				t.Fatalf("entries during the burst = %d, want %d", got, LoginFailureBurst)
			}
			*now = now.Add(LoginFailureWindow)
			s.flushLogins(ctx, false)
			full, sums := splitLoginEntries(t, loginFailedEntries(t, s))
			if full != LoginFailureBurst || len(sums) != 1 {
				t.Fatalf("entries = %d, summaries = %+v", full, sums)
			}
			got := sums[0]
			if got.Suppressed != tt.want.Suppressed || got.IP != tt.want.IP || got.Username != tt.want.Username ||
				got.Step != tt.want.Step || fmt.Sprint(got.Reasons) != fmt.Sprint(tt.want.Reasons) {
				t.Fatalf("summary = %+v, want %+v", got, tt.want)
			}
		})
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

// Distinct sources can't grow the tracker without bound: when every slot
// holds a live window, the oldest is ended early and its summary written.
func TestWriteLoginFailedKeyCap(t *testing.T) {
	ctx := context.Background()
	s, _, now := newLoginService(t)
	victim := LoginFailure{IP: "192.0.2.1", Account: "admin", Reason: LoginReasonLocked}
	for range LoginFailureBurst + 1 {
		s.logins.admit(victim)
	}
	*now = now.Add(time.Millisecond)
	for i := range maxLoginKeys/2 - 1 {
		s.logins.admit(LoginFailure{IP: fmt.Sprintf("10.0.%d.%d", i/250, i%250), Account: fmt.Sprintf("u%d", i), Reason: LoginReasonInvalid})
	}
	if n := len(s.logins.buckets); n != maxLoginKeys {
		t.Fatalf("tracked keys before the cap = %d", n)
	}
	s.WriteLoginFailed(ctx, LoginFailure{IP: "198.51.100.1", Account: "new", Reason: LoginReasonInvalid}, "new", nil)
	if n := len(s.logins.buckets); n > maxLoginKeys {
		t.Fatalf("tracked keys = %d", n)
	}
	entries := loginFailedEntries(t, s)
	if len(entries) != 2 || entries[0].Target != "new" {
		t.Fatalf("entries = %+v, want the evicted summary and the new attempt", entries)
	}
	_, sums := splitLoginEntries(t, entries[1:])
	if d := sums[0]; d.Suppressed != 1 || d.IP != "192.0.2.1" || d.Usernames["admin"] != 1 {
		t.Fatalf("evicted summary = %+v", d)
	}
}
