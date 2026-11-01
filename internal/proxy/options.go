package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SiteOptions are the structured per-site settings a route can carry on
// top of host → upstream. The zero value is Caddy's defaults with HTTP
// redirected to HTTPS.
type SiteOptions struct {
	Routing     RoutingOptions     `json:"routing"`
	HTTPS       HTTPSOptions       `json:"https"`
	Performance PerformanceOptions `json:"performance"`
	Access      AccessOptions      `json:"access"`
	Headers     HeaderOptions      `json:"headers"`
	Logs        LogOptions         `json:"logs"`
}

type RoutingOptions struct {
	// ExtraUpstreams are load-balanced together with the route's
	// upstream.
	ExtraUpstreams []string `json:"extra_upstreams"`
	LBPolicy       string   `json:"lb_policy"`
	HealthPath     string   `json:"health_path"`
	WWWRedirect    bool     `json:"www_redirect"`
}

type HTTPSOptions struct {
	// AllowHTTP serves a TLS site on plain HTTP too instead of
	// redirecting to HTTPS.
	AllowHTTP bool `json:"allow_http"`
	HSTS      bool `json:"hsts"`
}

type PerformanceOptions struct {
	FlushImmediately bool   `json:"flush_immediately"`
	BufferRequests   bool   `json:"buffer_requests"`
	DialTimeout      string `json:"dial_timeout"`
	ReadTimeout      string `json:"read_timeout"`
	WriteTimeout     string `json:"write_timeout"`
	MaxBodyBytes     int64  `json:"max_body_bytes"`
	StreamCloseDelay string `json:"stream_close_delay"`
	Compression      bool   `json:"compression"`
}

type AccessOptions struct {
	BasicAuth   []BasicAuthUser `json:"basic_auth"`
	IPMode      string          `json:"ip_mode"`
	IPRanges    []string        `json:"ip_ranges"`
	ForwardAuth *ForwardAuth    `json:"forward_auth,omitempty"`
}

// BasicAuthUser carries a plaintext Password only on the way in; the
// bcrypt Hash is stored and never leaves the service (see Public).
type BasicAuthUser struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// ForwardAuth's URL query can carry a token, so it is write-only: API
// responses leave it out and set QuerySet, and a URL sent without a
// query keeps the stored one unless ClearQuery is set.
type ForwardAuth struct {
	URL         string   `json:"url"`
	CopyHeaders []string `json:"copy_headers"`
	QuerySet    bool     `json:"query_set,omitempty"`
	ClearQuery  bool     `json:"clear_query,omitempty"`
}

type HeaderOptions struct {
	Security       bool     `json:"security"`
	RequestSet     []Header `json:"request_set"`
	RequestRemove  []string `json:"request_remove"`
	ResponseSet    []Header `json:"response_set"`
	ResponseRemove []string `json:"response_remove"`
}

// Header values marked Secret are write-only: API responses carry an
// empty Value and set ValueSet, and a secret header sent with an empty
// value keeps the stored one.
type Header struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Secret   bool   `json:"secret,omitempty"`
	ValueSet bool   `json:"value_set,omitempty"`
}

type LogOptions struct {
	AccessLog bool `json:"access_log"`
}

// decodeOptions reads stored or submitted options; absent fields keep
// their default.
func decodeOptions(raw []byte) (SiteOptions, error) {
	var o SiteOptions
	if len(raw) == 0 {
		return o, nil
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return SiteOptions{}, err
	}
	return o, nil
}

// ParseOptions decodes options sent by a client. Nil for an absent or
// null value, which callers treat as "keep" or "defaults".
func ParseOptions(raw []byte) (*SiteOptions, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	o, err := decodeOptions(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOptions, err)
	}
	return &o, nil
}

// Public returns a copy without password hashes, secret header values
// and the forward-auth query, and with every list present (empty rather
// than null), for API responses.
func (o SiteOptions) Public() SiteOptions {
	users := make([]BasicAuthUser, len(o.Access.BasicAuth))
	for i, u := range o.Access.BasicAuth {
		users[i] = BasicAuthUser{Username: u.Username}
	}
	o.Access.BasicAuth = users
	o.Routing.ExtraUpstreams = orEmpty(o.Routing.ExtraUpstreams)
	o.Access.IPRanges = orEmpty(o.Access.IPRanges)
	if f := o.Access.ForwardAuth; f != nil {
		fa := *f
		fa.CopyHeaders = orEmpty(fa.CopyHeaders)
		fa.URL, fa.QuerySet = splitQuery(f.URL)
		fa.ClearQuery = false
		o.Access.ForwardAuth = &fa
	}
	o.Headers.RequestSet = publicHeaders(o.Headers.RequestSet)
	o.Headers.RequestRemove = orEmpty(o.Headers.RequestRemove)
	o.Headers.ResponseSet = publicHeaders(o.Headers.ResponseSet)
	o.Headers.ResponseRemove = orEmpty(o.Headers.ResponseRemove)
	return o
}

func publicHeaders(in []Header) []Header {
	out := make([]Header, len(in))
	for i, h := range in {
		out[i] = Header{Name: h.Name, Value: h.Value, Secret: h.Secret}
		if h.Secret {
			out[i].Value, out[i].ValueSet = "", h.Value != ""
		}
	}
	return out
}

// splitQuery returns raw without its query and whether it had one. A URL
// that doesn't parse comes back empty, so nothing of it can leak.
func splitQuery(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	had := u.RawQuery != "" || u.ForceQuery
	u.RawQuery, u.ForceQuery = "", false
	return u.String(), had
}

// withoutSecrets returns a copy for the config preview: secret header
// values and the forward-auth query are replaced by maskedValue, so the
// preview shows where they go without showing them.
func (o SiteOptions) withoutSecrets() SiteOptions {
	hide := func(in []Header) []Header {
		out := make([]Header, len(in))
		for i, h := range in {
			out[i] = h
			if h.Secret {
				out[i].Value = maskedValue
			}
		}
		return out
	}
	o.Headers.RequestSet = hide(o.Headers.RequestSet)
	o.Headers.ResponseSet = hide(o.Headers.ResponseSet)
	if f := o.Access.ForwardAuth; f != nil {
		fa := *f
		base, had := splitQuery(f.URL)
		fa.URL = base
		if had {
			fa.URL += "?" + maskedValue
		}
		o.Access.ForwardAuth = &fa
	}
	return o
}

func orEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

// maskedValue stands in for values a caller may not see.
const maskedValue = "********"

// Masked returns a copy with header values and the forward-auth URL's
// query hidden, since both can carry credentials.
func (o SiteOptions) Masked() SiteOptions {
	o.Headers.RequestSet = maskHeaders(o.Headers.RequestSet)
	o.Headers.ResponseSet = maskHeaders(o.Headers.ResponseSet)
	if f := o.Access.ForwardAuth; f != nil {
		m := *f
		m.URL = ""
		if u, err := url.Parse(f.URL); err == nil {
			u.RawQuery, u.ForceQuery = "", false
			m.URL = u.String()
		}
		o.Access.ForwardAuth = &m
	}
	return o
}

func maskHeaders(in []Header) []Header {
	out := make([]Header, len(in))
	for i, h := range in {
		out[i] = Header{Name: h.Name, Value: maskedValue, Secret: h.Secret}
	}
	return out
}

// ErrInvalidOptions wraps every validation failure of SiteOptions.
var ErrInvalidOptions = errors.New("invalid proxy options")

// OptionError names the offending field so the API can say what to fix.
type OptionError struct {
	Field string
	Msg   string
}

func (e *OptionError) Error() string { return "options." + e.Field + ": " + e.Msg }
func (e *OptionError) Unwrap() error { return ErrInvalidOptions }

func optErr(field, format string, args ...any) error {
	return &OptionError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

const (
	maxExtraUpstreams = 16
	maxListEntries    = 32
	maxIPRanges       = 256
	maxTimeout        = 7 * 24 * time.Hour
	// requestBufferSize is how much of a request body Caddy reads before
	// it starts sending it upstream when request buffering is on.
	requestBufferSize = 4 << 20
	bcryptCost        = 12
)

var lbPolicies = map[string]bool{"": true, "round_robin": true, "least_conn": true, "first": true, "client_ip_hash": true}

// headerNameRe is an RFC 9110 token without `*`, which Caddy reads as
// "every header" in deletes.
var headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'+.^_`|~-]{1,128}$")

// bcryptHashRe is a complete bcrypt hash. The cost is capped because
// Caddy verifies it on every request with credentials; anything else in
// the string would reach Caddy, which expands placeholders in it.
var bcryptHashRe = regexp.MustCompile(`^\$2[aby]\$(0[4-9]|1[0-4])\$[./A-Za-z0-9]{53}$`)

// hasPlaceholderOrControl refuses values Caddy would expand: a `{...}`
// placeholder in a header can read files and environment variables of
// the proxy container.
func hasPlaceholderOrControl(s string) bool {
	return strings.ContainsAny(s, "{}") || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// validate checks the options of one site. It works on stored options
// too, so every basic-auth user must already carry a hash.
func (o *SiteOptions) validate(host, tlsMode string) error {
	if err := o.Routing.validate(host); err != nil {
		return err
	}
	if o.HTTPS.HSTS && tlsMode == "none" {
		return optErr("https.hsts", "needs HTTPS; the site's tls_mode is none")
	}
	if err := o.Performance.validate(); err != nil {
		return err
	}
	if err := o.Access.validate(); err != nil {
		return err
	}
	return o.Headers.validate()
}

func (r *RoutingOptions) validate(host string) error {
	if len(r.ExtraUpstreams) > maxExtraUpstreams {
		return optErr("routing.extra_upstreams", "at most %d", maxExtraUpstreams)
	}
	seen := map[string]bool{}
	for _, u := range r.ExtraUpstreams {
		if err := checkUpstream(u); err != nil {
			return optErr("routing.extra_upstreams", "%q: %v", u, err)
		}
		if seen[u] {
			return optErr("routing.extra_upstreams", "%q is listed twice", u)
		}
		seen[u] = true
	}
	if !lbPolicies[r.LBPolicy] {
		return optErr("routing.lb_policy", "must be one of round_robin, least_conn, first, client_ip_hash")
	}
	if r.HealthPath != "" {
		if !strings.HasPrefix(r.HealthPath, "/") || len(r.HealthPath) > 256 ||
			strings.ContainsAny(r.HealthPath, " #") || hasPlaceholderOrControl(r.HealthPath) {
			return optErr("routing.health_path", "must be a path like /health")
		}
	}
	if r.WWWRedirect {
		h := strings.ToLower(host)
		if strings.HasPrefix(h, "www.") || strings.HasPrefix(h, "*.") || net.ParseIP(strings.Trim(h, "[]")) != nil {
			return optErr("routing.www_redirect", "needs a host name without www. or wildcard")
		}
	}
	return nil
}

func (p *PerformanceOptions) validate() error {
	for _, d := range []struct{ field, v string }{
		{"performance.dial_timeout", p.DialTimeout},
		{"performance.read_timeout", p.ReadTimeout},
		{"performance.write_timeout", p.WriteTimeout},
		{"performance.stream_close_delay", p.StreamCloseDelay},
	} {
		if _, err := parseOptDuration(d.field, d.v); err != nil {
			return err
		}
	}
	if p.MaxBodyBytes < 0 {
		return optErr("performance.max_body_bytes", "must be 0 (no limit) or a size in bytes")
	}
	return nil
}

// parseOptDuration accepts "" (Caddy default) or a Go duration between
// 1s and 7 days.
func parseOptDuration(field, v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second || d > maxTimeout {
		return 0, optErr(field, "must be a duration from 1s to 168h, like 30s, 5m or 1h")
	}
	return d, nil
}

func (a *AccessOptions) validate() error {
	if len(a.BasicAuth) > maxListEntries {
		return optErr("access.basic_auth", "at most %d users", maxListEntries)
	}
	seen := map[string]bool{}
	for _, u := range a.BasicAuth {
		if u.Username == "" || len(u.Username) > 64 || strings.Contains(u.Username, ":") || hasPlaceholderOrControl(u.Username) {
			return optErr("access.basic_auth", "username %q: 1-64 characters, no colon, braces or control characters", u.Username)
		}
		if seen[u.Username] {
			return optErr("access.basic_auth", "username %q is listed twice", u.Username)
		}
		seen[u.Username] = true
		if !bcryptHashRe.MatchString(u.Hash) {
			return optErr("access.basic_auth", "user %q has no password", u.Username)
		}
	}
	switch a.IPMode {
	case "", "allow", "deny":
	default:
		return optErr("access.ip_mode", "must be empty (everyone), allow or deny")
	}
	if len(a.IPRanges) > maxIPRanges {
		return optErr("access.ip_ranges", "at most %d entries", maxIPRanges)
	}
	for _, r := range a.IPRanges {
		if _, err := parseIPRange(r); err != nil {
			return optErr("access.ip_ranges", "%q is not an IP address or CIDR range", r)
		}
	}
	if a.IPMode != "" && len(a.IPRanges) == 0 {
		return optErr("access.ip_ranges", "list at least one address when ip_mode is %s", a.IPMode)
	}
	if a.ForwardAuth != nil {
		if _, err := a.ForwardAuth.target(); err != nil {
			return err
		}
		if len(a.ForwardAuth.CopyHeaders) > maxListEntries {
			return optErr("access.forward_auth.copy_headers", "at most %d headers", maxListEntries)
		}
		for _, h := range a.ForwardAuth.CopyHeaders {
			if !headerNameRe.MatchString(h) {
				return optErr("access.forward_auth.copy_headers", "%q is not a header name", h)
			}
		}
	}
	return nil
}

func parseIPRange(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, errors.New("invalid")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

type forwardAuthTarget struct {
	dial string
	uri  string
	tls  bool
}

// target splits the forward-auth URL into what Caddy needs: the address
// to dial, the request URI to send and whether to use TLS.
func (f *ForwardAuth) target() (forwardAuthTarget, error) {
	const field = "access.forward_auth.url"
	u, err := url.Parse(f.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return forwardAuthTarget{}, optErr(field, "must be an http(s) URL like http://authelia:9091/api/verify")
	}
	if hasPlaceholderOrControl(f.URL) || strings.Contains(f.URL, " ") {
		return forwardAuthTarget{}, optErr(field, "must not contain spaces, braces or control characters")
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	dial := net.JoinHostPort(host, port)
	if err := checkUpstream(dial); err != nil {
		return forwardAuthTarget{}, optErr(field, "%v", err)
	}
	uri := u.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	if u.RawQuery != "" {
		uri += "?" + u.RawQuery
	}
	return forwardAuthTarget{dial: dial, uri: uri, tls: u.Scheme == "https"}, nil
}

func (h *HeaderOptions) validate() error {
	for _, l := range []struct {
		field string
		set   []Header
		names []string
	}{
		{"headers.request_set", h.RequestSet, nil},
		{"headers.request_remove", nil, h.RequestRemove},
		{"headers.response_set", h.ResponseSet, nil},
		{"headers.response_remove", nil, h.ResponseRemove},
	} {
		if len(l.set) > maxListEntries || len(l.names) > maxListEntries {
			return optErr(l.field, "at most %d headers", maxListEntries)
		}
		for _, hd := range l.set {
			if !headerNameRe.MatchString(hd.Name) {
				return optErr(l.field, "%q is not a header name", hd.Name)
			}
			if len(hd.Value) > 4096 || hasPlaceholderOrControl(hd.Value) {
				return optErr(l.field, "value of %s must be up to 4096 characters without braces or control characters", hd.Name)
			}
		}
		for _, n := range l.names {
			if !headerNameRe.MatchString(n) {
				return optErr(l.field, "%q is not a header name", n)
			}
		}
	}
	return nil
}

// applyPasswords turns submitted passwords into bcrypt hashes. A user
// submitted without a password keeps the hash stored under the same
// name in prev; hashes sent by the client are ignored.
func (o *SiteOptions) applyPasswords(prev *SiteOptions) error {
	old := map[string]string{}
	if prev != nil {
		for _, u := range prev.Access.BasicAuth {
			old[u.Username] = u.Hash
		}
	}
	for i := range o.Access.BasicAuth {
		u := &o.Access.BasicAuth[i]
		u.Hash = ""
		if u.Password == "" {
			u.Hash = old[u.Username]
			if u.Hash == "" {
				return optErr("access.basic_auth", "user %q needs a password", u.Username)
			}
			continue
		}
		if len(u.Password) < 8 || len(u.Password) > 72 {
			return optErr("access.basic_auth", "password of %q must be 8 to 72 bytes", u.Username)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(u.Password), bcryptCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		u.Hash = string(hash)
		u.Password = ""
	}
	return nil
}

// applySecrets fills write-only values the client left empty from prev:
// secret header values by list and name, and the forward-auth query as
// long as the URL still points at the same server, since the query would
// otherwise go to a host the stored token was never meant for.
func (o *SiteOptions) applySecrets(prev *SiteOptions) error {
	var prevOpts SiteOptions
	if prev != nil {
		prevOpts = *prev
	}
	for _, l := range []struct {
		field     string
		set, prev []Header
	}{
		{"headers.request_set", o.Headers.RequestSet, prevOpts.Headers.RequestSet},
		{"headers.response_set", o.Headers.ResponseSet, prevOpts.Headers.ResponseSet},
	} {
		for i := range l.set {
			h := &l.set[i]
			h.ValueSet = false
			if !h.Secret || h.Value != "" {
				continue
			}
			for _, p := range l.prev {
				if strings.EqualFold(p.Name, h.Name) {
					h.Value = p.Value
					break
				}
			}
			if h.Value == "" {
				return optErr(l.field, "secret header %s needs a value", h.Name)
			}
		}
	}
	f := o.Access.ForwardAuth
	if f == nil {
		return nil
	}
	drop := f.ClearQuery
	f.QuerySet, f.ClearQuery = false, false
	pf := prevOpts.Access.ForwardAuth
	if drop || pf == nil {
		return nil
	}
	u, err := url.Parse(f.URL)
	if err != nil || u.RawQuery != "" || u.ForceQuery {
		return nil
	}
	old, err := url.Parse(pf.URL)
	if err != nil || old.RawQuery == "" {
		return nil
	}
	if !strings.EqualFold(u.Scheme, old.Scheme) || !strings.EqualFold(u.Host, old.Host) {
		return optErr("access.forward_auth.url", "the stored query is only kept for the same server; enter it again or clear it")
	}
	u.RawQuery = old.RawQuery
	f.URL = u.String()
	return nil
}

// ChangedGroups names the option groups that differ between a and b, as
// "options.<group>", for audit entries. Secrets count: a new password or
// header value changes its group.
func ChangedGroups(a, b SiteOptions) []string {
	a, b = a.canonical(), b.canonical()
	var out []string
	for _, g := range []struct {
		name string
		a, b any
	}{
		{"routing", a.Routing, b.Routing},
		{"https", a.HTTPS, b.HTTPS},
		{"performance", a.Performance, b.Performance},
		{"access", a.Access, b.Access},
		{"headers", a.Headers, b.Headers},
		{"logs", a.Logs, b.Logs},
	} {
		ja, _ := json.Marshal(g.a)
		jb, _ := json.Marshal(g.b)
		if string(ja) != string(jb) {
			out = append(out, "options."+g.name)
		}
	}
	return out
}

// canonical turns absent lists into empty ones, so stored and submitted
// options compare equal when only that differs.
func (o SiteOptions) canonical() SiteOptions {
	o.Routing.ExtraUpstreams = orEmpty(o.Routing.ExtraUpstreams)
	o.Access.BasicAuth = orEmpty(o.Access.BasicAuth)
	o.Access.IPRanges = orEmpty(o.Access.IPRanges)
	if f := o.Access.ForwardAuth; f != nil {
		fa := *f
		fa.CopyHeaders = orEmpty(fa.CopyHeaders)
		o.Access.ForwardAuth = &fa
	}
	o.Headers.RequestSet = orEmpty(o.Headers.RequestSet)
	o.Headers.RequestRemove = orEmpty(o.Headers.RequestRemove)
	o.Headers.ResponseSet = orEmpty(o.Headers.ResponseSet)
	o.Headers.ResponseRemove = orEmpty(o.Headers.ResponseRemove)
	return o
}

// normalize stores IP ranges in canonical CIDR form.
func (o *SiteOptions) normalize() {
	for i, r := range o.Access.IPRanges {
		if p, err := parseIPRange(r); err == nil {
			o.Access.IPRanges[i] = p.String()
		}
	}
	o.Access.IPRanges = dedupe(o.Access.IPRanges)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func durationNanos(v string) int64 {
	d, _ := time.ParseDuration(v)
	return int64(d)
}
