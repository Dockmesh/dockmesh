package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

// accountKeysMax caps the per-account counters of one authLimits; account
// names come from the client.
const accountKeysMax = 100_000

// authLimits throttles failed attempts at one sign-in step, both per client
// IP (spraying many accounts) and per account (one account from many IPs).
type authLimits struct {
	byIP      *ratelimit.Limiter
	byAccount *ratelimit.Limiter
}

// newAuthLimits allows ipMax / accountMax failures per window before locking
// the key for the same duration. accountMax 0 disables the account limit.
func newAuthLimits(ipMax, accountMax int, window time.Duration) authLimits {
	l := authLimits{byIP: ratelimit.New(ipMax, window, window)}
	if accountMax > 0 {
		l.byAccount = ratelimit.New(accountMax, window, window)
		l.byAccount.MaxKeys = accountKeysMax
	}
	return l
}

// authAttempt is an attempt counted by authLimits.acquire.
type authAttempt struct {
	ip, account       ratelimit.Ticket
	hasIP, hasAccount bool
	accountKey        string
}

// acquire counts an attempt from ip and for account before it is checked,
// and writes 429 with Retry-After when either is blocked. An empty ip or
// account skips that side. Every granted attempt must be finished.
func (l authLimits) acquire(w http.ResponseWriter, ip, account string) (authAttempt, bool) {
	var a authAttempt
	if l.byIP != nil && ip != "" {
		t, ok, retry := l.byIP.Acquire(clientip.LimitKey(ip))
		if !ok {
			writeTooMany(w, "too many attempts", retry)
			return a, false
		}
		a.ip, a.hasIP = t, true
	}
	if l.byAccount != nil && account != "" {
		sum := sha256.Sum256([]byte(account))
		a.accountKey = hex.EncodeToString(sum[:])
		t, ok, retry := l.byAccount.Acquire(a.accountKey)
		if !ok {
			l.finish(a, false)
			writeTooMany(w, "too many attempts", retry)
			return a, false
		}
		a.account, a.hasAccount = t, true
	}
	return a, true
}

// finish settles a failed attempt. Otherwise the IP side is refunded and
// the account bucket cleared: a success on one account must not reset the
// IP bucket another account is being guessed from.
func (l authLimits) finish(a authAttempt, failed bool) {
	if failed {
		if a.hasIP {
			l.byIP.Settle(a.ip)
		}
		if a.hasAccount {
			l.byAccount.Settle(a.account)
		}
		return
	}
	if a.hasIP {
		l.byIP.Refund(a.ip)
	}
	if a.hasAccount {
		l.byAccount.Succeed(a.accountKey)
	}
}

// limitBody caps request bodies on the unauthenticated sign-in endpoints;
// their keys and payloads come straight from the client.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
}
