package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTestCode stores a fresh code in path the way new-code does, minus
// the chown, and returns the plaintext.
func writeTestCode(t *testing.T, path string, now time.Time) string {
	t.Helper()
	code, err := NewCode()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := NewCodeRecord(code, now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return code
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestCodes(t *testing.T) (*Codes, string, *fakeClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setup-code.hash")
	clock := &fakeClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	c := NewCodes(path)
	c.now = clock.now
	return c, path, clock
}

func redeemErr(t *testing.T, err error) *RedeemError {
	t.Helper()
	var re *RedeemError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want a RedeemError", err)
	}
	return re
}

func TestNewCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != CodeLength {
			t.Fatalf("code %q has %d characters", code, len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("code %q has %q, not Crockford base32", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("code %q repeated", code)
		}
		seen[code] = true
		if got, ok := NormalizeCode(FormatCode(code)); !ok || got != code {
			t.Fatalf("formatted code %q does not normalize back: %q %v", FormatCode(code), got, ok)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{in: "K7QD-3MXA-9PRT", want: "K7QD3MXA9PRT", ok: true},
		{in: "k7qd 3mxa 9prt", want: "K7QD3MXA9PRT", ok: true},
		{in: "K7QD-3MXA-9PRTI", ok: false},
		{in: "OIL0-0000-0000", want: "011000000000", ok: true},
		{in: "K7QD-3MXA-9PR", ok: false},
		{in: "K7QD-3MXA-9PRU", ok: false},
		{in: "K7QD-3MXA-9PRÜ", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := NormalizeCode(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("NormalizeCode(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestCodeRecordHasNoPlaintext(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), code) || strings.Contains(string(data), FormatCode(code)) {
		t.Fatal("the code file holds the plaintext code")
	}
	if !strings.Contains(string(data), "$argon2id$") {
		t.Fatalf("code file = %s, want an argon2id hash", data)
	}
	if c.State() != CodeReady {
		t.Fatalf("state = %v", c.State())
	}
}

func TestRedeemOpensOneSession(t *testing.T) {
	c, path, clock := newTestCodes(t)
	if _, err := c.Redeem("192.0.2.1", "K7QD-3MXA-9PRT"); redeemErr(t, err).State != CodeNone {
		t.Fatal("a code was accepted with no code file")
	}
	code := writeTestCode(t, path, clock.t)
	first, err := c.Redeem("192.0.2.1", strings.ToLower(FormatCode(code)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Session(first) != SessionWrite {
		t.Fatalf("session = %v", c.Session(first))
	}
	second, err := c.Redeem("192.0.2.2", code)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Session(first); got != SessionReplaced {
		t.Fatalf("first session after a second entry = %v, want replaced", got)
	}
	if c.Session(second) != SessionWrite {
		t.Fatal("second entry did not take the session over")
	}
	if c.Session("forged") != SessionNone || c.Session("") != SessionNone {
		t.Fatal("an unknown token counts as a session")
	}
}

func TestRedeemPerAddressLimit(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	for i := range 5 {
		re := redeemErr(t, func() error { _, err := c.Redeem("192.0.2.1", "AAAA-AAAA-AAAA"); return err }())
		if re.RateLimited || re.AttemptsLeft != 4-i {
			t.Fatalf("attempt %d: %+v", i+1, re)
		}
	}
	_, err := c.Redeem("192.0.2.1", code)
	re := redeemErr(t, err)
	if !re.RateLimited || re.RetryAfter <= 0 || re.RetryAfter > 15*time.Minute {
		t.Fatalf("sixth attempt with the right code: %+v", re)
	}
	if _, err := c.Redeem("192.0.2.9", code); err != nil {
		t.Fatalf("another address is blocked too: %v", err)
	}
	clock.t = clock.t.Add(15*time.Minute + time.Second)
	if _, err := c.Redeem("192.0.2.1", code); err != nil {
		t.Fatalf("address still blocked after 15 minutes: %v", err)
	}
}

func TestRedeemLocksAfterTwentyFailures(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	token, err := c.Redeem("192.0.2.200", code)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		ip := "192.0.2." + string(rune('a'+i/5))
		if _, err := c.Redeem(ip, "AAAA-AAAA-AAAA"); err == nil {
			t.Fatal("wrong code accepted")
		}
	}
	if c.State() != CodeLocked {
		t.Fatalf("state after 20 failures = %v, want locked", c.State())
	}
	if _, err := c.Redeem("192.0.2.250", code); redeemErr(t, err).State != CodeLocked {
		t.Fatal("the right code works after 20 failures")
	}
	if c.Session(token) != SessionNone {
		t.Fatal("the session outlives its locked code")
	}
	next := writeTestCode(t, path, clock.t)
	if c.State() != CodeReady {
		t.Fatalf("state after new-code = %v", c.State())
	}
	if _, err := c.Redeem("192.0.2.250", next); err != nil {
		t.Fatalf("new code refused: %v", err)
	}
}

func TestCodeExpiry(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	token, err := c.Redeem("192.0.2.1", code)
	if err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(CodeLifetime - time.Second)
	if c.Session(token) != SessionWrite {
		t.Fatal("session ended before the code expired")
	}
	clock.t = clock.t.Add(time.Second)
	if c.State() != CodeExpired {
		t.Fatalf("state at 2 h = %v, want expired", c.State())
	}
	if c.Session(token) != SessionNone {
		t.Fatal("session outlives its code")
	}
	if _, err := c.Redeem("192.0.2.1", code); redeemErr(t, err).State != CodeExpired {
		t.Fatal("expired code accepted")
	}
}

func TestNewCodeEndsOldCodeAndSession(t *testing.T) {
	c, path, clock := newTestCodes(t)
	old := writeTestCode(t, path, clock.t)
	token, err := c.Redeem("192.0.2.1", old)
	if err != nil {
		t.Fatal(err)
	}
	writeTestCode(t, path, clock.t)
	if c.Session(token) != SessionNone {
		t.Fatal("session of the old code survives new-code")
	}
	if _, err := c.Redeem("192.0.2.1", old); err == nil {
		t.Fatal("old code still works")
	}
}

func TestCommittedSessionOnlyFollowsTheInstall(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	token, err := c.Redeem("192.0.2.1", code)
	if err != nil {
		t.Fatal(err)
	}
	if c.BeginCommit("other") {
		t.Fatal("commit without the session")
	}
	if !c.BeginCommit(token) {
		t.Fatal("commit refused")
	}
	if c.Session(token) != SessionReadOnly {
		t.Fatalf("session while installing = %v", c.Session(token))
	}
	clock.t = clock.t.Add(3 * time.Hour)
	if c.Session(token) != SessionReadOnly {
		t.Fatal("installing session lost its read access when the code expired")
	}
	c.EndCommit(false)
	if c.Session(token) != SessionNone {
		t.Fatal("failed install may retry after the code expired")
	}
}

func TestFailedCommitRetriesWhileCodeValid(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	token, _ := c.Redeem("192.0.2.1", code)
	if !c.BeginCommit(token) {
		t.Fatal("commit refused")
	}
	c.EndCommit(false)
	if c.Session(token) != SessionWrite {
		t.Fatal("failed install lost its write access while the code is valid")
	}
}

func TestUntrustedCodeFileIgnored(t *testing.T) {
	c, path, clock := newTestCodes(t)
	code := writeTestCode(t, path, clock.t)
	if err := os.Chmod(path, 0o662); err != nil {
		t.Fatal(err)
	}
	if c.State() != CodeNone {
		t.Fatalf("group-writable code file accepted: %v", c.State())
	}
	if _, err := c.Redeem("192.0.2.1", code); err == nil {
		t.Fatal("code from a group-writable file accepted")
	}
}

func TestParallelGuessesStayWithinLimit(t *testing.T) {
	c, path, clock := newTestCodes(t)
	writeTestCode(t, path, clock.t)
	const n = 20
	verified := make(chan bool, n)
	for range n {
		go func() {
			_, err := c.Redeem("192.0.2.1", "AAAA-AAAA-AAAA")
			var re *RedeemError
			verified <- errors.As(err, &re) && !re.RateLimited
		}()
	}
	checked := 0
	for range n {
		if <-verified {
			checked++
		}
	}
	if checked > 5 {
		t.Fatalf("%d parallel guesses from one address were checked, want at most 5", checked)
	}
}
