package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

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
	}
	return l
}

// allow writes 429 with Retry-After and returns false while the IP or the
// account is locked. An empty account skips the account check.
func (l authLimits) allow(w http.ResponseWriter, ip, account string) bool {
	ip = clientip.LimitKey(ip)
	retry := time.Duration(0)
	if l.byIP != nil {
		if ok, d := l.byIP.Check(ip); !ok {
			retry = d
		}
	}
	if l.byAccount != nil && account != "" {
		if ok, d := l.byAccount.Check(account); !ok && d > retry {
			retry = d
		}
	}
	if retry == 0 {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, "too many attempts — try again later")
	return false
}

func (l authLimits) fail(ip, account string) {
	if l.byIP != nil {
		l.byIP.Fail(clientip.LimitKey(ip))
	}
	if l.byAccount != nil && account != "" {
		l.byAccount.Fail(account)
	}
}

// limitBody caps request bodies on the unauthenticated sign-in endpoints;
// their keys and payloads come straight from the client.
func limitBody(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
}

// succeed clears the account bucket only: a success on one account must not
// reset the IP bucket another account is being guessed from.
func (l authLimits) succeed(account string) {
	if l.byAccount != nil && account != "" {
		l.byAccount.Succeed(account)
	}
}
