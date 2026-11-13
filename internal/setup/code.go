package setup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/layout"
)

const (
	// CodeLifetime is how long a setup code is valid after `new-code`.
	CodeLifetime = 2 * time.Hour
	// CodeLength is the number of Crockford base32 characters (60 bits).
	CodeLength = 12
	// CodeCommand prints a new setup code on the server.
	CodeCommand = "sudo dockmesh setup new-code"

	codeIPLimit    = 5
	codeIPWindow   = 15 * time.Minute
	codeTotalLimit = 20
	maxCodeFile    = 4 << 10
)

// CodeFile holds the argon2id hash and expiry of the current setup code,
// root:dockmesh 0640. The plaintext is never stored.
const CodeFile = layout.ConfigDir + "/setup-code.hash"

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewCode returns a random setup code of CodeLength characters.
func NewCode() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	out := make([]byte, CodeLength)
	for i := range out {
		out[i] = crockford[n&31]
		n >>= 5
	}
	return string(out), nil
}

// FormatCode groups a code for display: K7QD-3MXA-9PRT.
func FormatCode(code string) string {
	if len(code) != CodeLength {
		return code
	}
	return code[:4] + "-" + code[4:8] + "-" + code[8:]
}

// NormalizeCode accepts what a person types: any case, dashes and spaces,
// and the Crockford look-alikes I, L (1) and O (0).
func NormalizeCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if r > 127 || !strings.ContainsRune(crockford, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != CodeLength {
		return "", false
	}
	return b.String(), true
}

// CodeRecord is the content of CodeFile.
type CodeRecord struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewCodeRecord hashes code for CodeFile.
func NewCodeRecord(code string, now time.Time) (CodeRecord, error) {
	h, err := auth.HashPassword(code)
	if err != nil {
		return CodeRecord{}, err
	}
	return CodeRecord{Hash: h, ExpiresAt: now.Add(CodeLifetime).UTC()}, nil
}

// CodeState is what the code form shows.
type CodeState string

const (
	CodeNone    CodeState = "no_code"
	CodeReady   CodeState = "ready"
	CodeExpired CodeState = "expired"
	CodeLocked  CodeState = "locked"
)

// SessionState is what a setup session cookie is worth.
type SessionState string

const (
	SessionNone SessionState = "none"
	// SessionWrite may run every setup step.
	SessionWrite SessionState = "valid"
	// SessionReadOnly follows an install it started, after its code
	// expired or the install committed.
	SessionReadOnly SessionState = "read_only"
	// SessionReplaced was taken over by the code entered elsewhere.
	SessionReplaced SessionState = "replaced"
)

// RedeemError says why a code was refused.
type RedeemError struct {
	State CodeState
	// RateLimited: too many wrong codes from this address.
	RateLimited  bool
	RetryAfter   time.Duration
	AttemptsLeft int
}

func (e *RedeemError) Error() string {
	switch {
	case e.RateLimited:
		return "too many wrong setup codes from this address"
	case e.State != CodeReady:
		return "setup code " + string(e.State)
	default:
		return "wrong setup code"
	}
}

type codeSession struct {
	token     [sha256.Size]byte
	code      string
	committed bool
}

// Codes checks setup codes against CodeFile and keeps the one setup
// session. Counters live in memory; the file is read on every check, so
// `new-code` takes effect without a restart.
type Codes struct {
	path  string
	now   func() time.Time
	trust func(fs.FileInfo) error

	verify sync.Mutex // one argon2id verification at a time

	mu       sync.Mutex
	ipFails  map[string][]time.Time
	failed   map[string]int
	logged   map[string]CodeState
	session  *codeSession
	replaced map[[sha256.Size]byte]bool
}

// NewCodes reads codes from path; empty means CodeFile.
func NewCodes(path string) *Codes {
	if path == "" {
		path = CodeFile
	}
	return &Codes{
		path:     path,
		now:      time.Now,
		trust:    trustCodeFile,
		ipFails:  map[string][]time.Time{},
		failed:   map[string]int{},
		logged:   map[string]CodeState{},
		replaced: map[[sha256.Size]byte]bool{},
	}
}

// read returns the current record, or false when there is no usable one.
func (c *Codes) read() (CodeRecord, bool) {
	f, err := os.Open(c.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("setup code file unreadable", "path", c.path, "err", err)
		}
		return CodeRecord{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err == nil {
		err = c.trust(fi)
	}
	if err != nil {
		slog.Warn("setup code file ignored", "path", c.path, "err", err)
		return CodeRecord{}, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCodeFile))
	if err != nil {
		return CodeRecord{}, false
	}
	var rec CodeRecord
	if err := json.Unmarshal(data, &rec); err != nil || !strings.HasPrefix(rec.Hash, "$argon2id$") {
		slog.Warn("setup code file ignored: not a setup code", "path", c.path)
		return CodeRecord{}, false
	}
	return rec, true
}

// stateLocked computes the code state; c.mu must be held.
func (c *Codes) stateLocked(rec CodeRecord, ok bool) CodeState {
	var st CodeState
	switch {
	case !ok:
		st = CodeNone
	case c.failed[rec.Hash] >= codeTotalLimit:
		st = CodeLocked
	case !c.now().Before(rec.ExpiresAt):
		st = CodeExpired
	default:
		st = CodeReady
	}
	if ok && st != CodeReady && c.logged[rec.Hash] != st {
		c.logged[rec.Hash] = st
		if st == CodeLocked {
			slog.Warn("setup code locked after 20 wrong attempts: get a new one with: " + CodeCommand)
		} else {
			slog.Warn("setup code expired: get a new one with: " + CodeCommand)
		}
	}
	return st
}

// State reports the current code's state.
func (c *Codes) State() CodeState {
	rec, ok := c.read()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked(rec, ok)
}

// Redeem checks code from ip. A right code opens the setup session and
// returns its token; the session of an earlier entry ends.
func (c *Codes) Redeem(ip, code string) (string, error) {
	rec, ok := c.read()
	now := c.now()
	c.mu.Lock()
	if st := c.stateLocked(rec, ok); st != CodeReady {
		c.mu.Unlock()
		return "", &RedeemError{State: st}
	}
	recent := c.ipFails[ip][:0]
	for _, t := range c.ipFails[ip] {
		if now.Sub(t) < codeIPWindow {
			recent = append(recent, t)
		}
	}
	c.ipFails[ip] = recent
	if len(recent) >= codeIPLimit {
		c.mu.Unlock()
		return "", &RedeemError{State: CodeReady, RateLimited: true, RetryAfter: codeIPWindow - now.Sub(recent[0])}
	}
	c.mu.Unlock()

	norm, valid := NormalizeCode(code)
	right := false
	if valid {
		c.verify.Lock()
		right, _ = auth.VerifyPassword(norm, rec.Hash)
		c.verify.Unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !right {
		c.ipFails[ip] = append(c.ipFails[ip], now)
		c.failed[rec.Hash]++
		left := codeIPLimit - len(c.ipFails[ip])
		if total := codeTotalLimit - c.failed[rec.Hash]; total < left {
			left = total
		}
		st := c.stateLocked(rec, true)
		return "", &RedeemError{State: st, AttemptsLeft: max(left, 0)}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if c.session != nil {
		c.replaced[c.session.token] = true
	}
	c.session = &codeSession{token: sha256.Sum256([]byte(token)), code: rec.Hash}
	return token, nil
}

// Session reports what token is worth now.
func (c *Codes) Session(token string) SessionState {
	if token == "" {
		return SessionNone
	}
	rec, ok := c.read()
	sum := sha256.Sum256([]byte(token))
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.session
	if s == nil || subtle.ConstantTimeCompare(s.token[:], sum[:]) != 1 {
		if c.replaced[sum] {
			return SessionReplaced
		}
		return SessionNone
	}
	if c.stateLocked(rec, ok) == CodeReady && rec.Hash == s.code && !s.committed {
		return SessionWrite
	}
	if s.committed {
		return SessionReadOnly
	}
	return SessionNone
}

// BeginCommit marks the session of token as installing, after which it
// can only follow the install. It fails unless token may write.
func (c *Codes) BeginCommit(token string) bool {
	if c.Session(token) != SessionWrite {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session.committed = true
	return true
}

// EndCommit gives a failed install its write rights back; they still end
// with the code.
func (c *Codes) EndCommit(success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil && !success {
		c.session.committed = false
	}
}

// WriteCodeRecord stores rec in path atomically, root:gid 0640.
func WriteCodeRecord(path string, rec CodeRecord, gid int) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeRootFile(path, data, gid)
}

func errUntrusted(format string, a ...any) error {
	return fmt.Errorf("untrusted setup code file: "+format, a...)
}
