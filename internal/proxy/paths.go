package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PathRoute sends the requests of a site that match one path somewhere
// else than the site's fallback: another backend, a redirect or a fixed
// answer. The site's access rules apply before any path route.
type PathRoute struct {
	Match  PathMatch `json:"match"`
	Action string    `json:"action"`
	// Proxy action.
	Target      PathTarget    `json:"target"`
	StripPrefix bool          `json:"strip_prefix"`
	Headers     PathHeaders   `json:"headers"`
	Transport   PathTransport `json:"transport"`
	// Redirect action.
	To   string `json:"to"`
	Code int    `json:"code"`
	// Respond action.
	Status int `json:"status"`
}

// PathMatch types.
const (
	MatchPrefix = "prefix"
	MatchExact  = "exact"
)

// Path route actions.
const (
	ActionProxy    = "proxy"
	ActionRedirect = "redirect"
	ActionRespond  = "respond"
)

// Target kinds of a proxy path route.
const (
	TargetSite   = "site"
	TargetStatic = "static"
)

// PathMatch is a path prefix (the path and everything below it) or one
// exact path.
type PathMatch struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

// PathTarget is where a proxy path route sends requests: the site's own
// upstreams with their settings, or addresses of its own.
type PathTarget struct {
	Kind       string      `json:"kind"`
	Upstreams  []string    `json:"upstreams"`
	LBPolicy   string      `json:"lb_policy"`
	HealthPath string      `json:"health_path"`
	TLS        UpstreamTLS `json:"tls"`
}

// PathHeaders are added after the site's headers of the same kind; on
// the same name the route's wins.
type PathHeaders struct {
	RequestSet     []Header `json:"request_set"`
	RequestRemove  []string `json:"request_remove"`
	ResponseSet    []Header `json:"response_set"`
	ResponseRemove []string `json:"response_remove"`
}

// PathTransport overrides the site's performance settings for one route;
// empty or nil fields keep the site's.
type PathTransport struct {
	DialTimeout      string `json:"dial_timeout"`
	ReadTimeout      string `json:"read_timeout"`
	WriteTimeout     string `json:"write_timeout"`
	FlushImmediately *bool  `json:"flush_immediately"`
	BufferRequests   *bool  `json:"buffer_requests"`
	StreamCloseDelay string `json:"stream_close_delay"`
}

const maxPathRoutes = 50

var (
	respondStatuses = map[int]bool{403: true, 404: true}
	pathActions     = map[string]bool{ActionProxy: true, ActionRedirect: true, ActionRespond: true}
)

// ParsePaths decodes the path list sent by a client. Nil for an absent
// or null value, which callers treat as "keep".
func ParsePaths(raw []byte) (*[]PathRoute, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var p []PathRoute
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: paths: %w", ErrInvalidOptions, err)
	}
	if p == nil {
		p = []PathRoute{}
	}
	return &p, nil
}

func decodePaths(raw string) ([]PathRoute, error) {
	if raw == "" {
		return []PathRoute{}, nil
	}
	var p []PathRoute
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	if p == nil {
		p = []PathRoute{}
	}
	return p, nil
}

// PathError names the offending field of a path route as
// paths[<i>].<field>, i being the position after sorting.
type PathError struct {
	Index int
	Field string
	Msg   string
}

func (e *PathError) Error() string {
	return fmt.Sprintf("paths[%d].%s: %s", e.Index, e.Field, e.Msg)
}

func (e *PathError) Unwrap() error { return ErrInvalidOptions }

// asPathError turns an OptionError from a shared checker into a
// PathError of route i.
func asPathError(i int, err error) error {
	var oe *OptionError
	if errors.As(err, &oe) {
		return &PathError{Index: i, Field: strings.TrimPrefix(oe.Field, "."), Msg: oe.Msg}
	}
	return err
}

// normalizePath trims a trailing slash, so /ords/ and /ords are one.
func normalizePath(p string) string {
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// sortPaths puts the routes in the order Caddy tries them: exact paths
// first, then longer prefixes before shorter ones, so no route can hide
// another.
func sortPaths(p []PathRoute) {
	sort.SliceStable(p, func(i, j int) bool {
		a, b := p[i].Match, p[j].Match
		if (a.Type == MatchExact) != (b.Type == MatchExact) {
			return a.Type == MatchExact
		}
		if len(a.Path) != len(b.Path) {
			return len(a.Path) > len(b.Path)
		}
		return a.Path < b.Path
	})
}

// normalizePaths cleans the input before validation: trailing slashes,
// CA text reduced to its certificates, and the fixed order.
func normalizePaths(p []PathRoute) {
	for i := range p {
		r := &p[i]
		r.Match.Path = normalizePath(r.Match.Path)
		if r.Target.TLS.CAPEM != "" {
			r.Target.TLS.CAPEM = normalizeCAPEM(r.Target.TLS.CAPEM)
		}
	}
	sortPaths(p)
}

// validatePaths checks a site's path list, which must already be sorted.
func validatePaths(p []PathRoute) error {
	if len(p) > maxPathRoutes {
		return &PathError{Index: maxPathRoutes, Field: "match.path", Msg: fmt.Sprintf("a site can have at most %d paths", maxPathRoutes)}
	}
	seen := map[string]bool{}
	for i := range p {
		if err := p[i].validate(); err != nil {
			return asPathError(i, err)
		}
		key := matchKey(p[i].Match)
		if seen[key] {
			return &PathError{Index: i, Field: "match.path", Msg: "this path is already in the list"}
		}
		seen[key] = true
	}
	return nil
}

func (r *PathRoute) validate() error {
	switch r.Match.Type {
	case MatchPrefix, MatchExact:
	default:
		return optErr("match.type", "must be prefix or exact")
	}
	if err := checkRoutePath(r.Match.Path); err != nil {
		return optErr("match.path", "%v", err)
	}
	if !pathActions[r.Action] {
		return optErr("action", "must be proxy, redirect or respond")
	}
	if r.Action != ActionProxy {
		if r.StripPrefix || r.Target.Kind != "" || !r.Headers.empty() || r.Transport != (PathTransport{}) {
			return optErr("action", "only a proxy route has a backend, headers or timeouts")
		}
	}
	if r.Action != ActionRedirect && (r.To != "" || r.Code != 0) {
		return optErr("to", "only a redirect has a target")
	}
	if r.Action != ActionRespond && r.Status != 0 {
		return optErr("status", "only a respond route has a status")
	}
	switch r.Action {
	case ActionRedirect:
		return checkRedirect("", r.To, r.Code)
	case ActionRespond:
		if !respondStatuses[r.Status] {
			return optErr("status", "must be 403 or 404")
		}
		return nil
	}
	if r.StripPrefix && r.Match.Type != MatchPrefix {
		return optErr("strip_prefix", "only a prefix can be stripped")
	}
	if err := r.Target.validate(); err != nil {
		return err
	}
	if err := r.Headers.validate(); err != nil {
		return err
	}
	return r.Transport.validate()
}

// checkRoutePath is checkURLPath plus what a matcher path may not be:
// a wildcard, a placeholder or the root, which is the fallback.
func checkRoutePath(p string) error {
	if p == "/" {
		return fmt.Errorf("use the fallback for /, not a path")
	}
	if strings.Contains(p, "*") {
		return fmt.Errorf("must not contain *")
	}
	return checkURLPath(p)
}

func (t *PathTarget) validate() error {
	switch t.Kind {
	case TargetSite:
		if len(t.Upstreams) > 0 || t.LBPolicy != "" || t.HealthPath != "" || t.TLS != (UpstreamTLS{}) {
			return optErr("target.kind", "a route to the site's backend uses the site's settings; pick static for other addresses")
		}
		return nil
	case TargetStatic:
	default:
		return optErr("target.kind", "must be site or static")
	}
	if len(t.Upstreams) == 0 {
		return optErr("target.upstreams", "list at least one host:port")
	}
	if len(t.Upstreams) > maxExtraUpstreams+1 {
		return optErr("target.upstreams", "at most %d", maxExtraUpstreams+1)
	}
	seen := map[string]bool{}
	for _, u := range t.Upstreams {
		if err := checkUpstream(u); err != nil {
			return optErr("target.upstreams", "%q: %v", u, err)
		}
		if seen[strings.ToLower(u)] {
			return optErr("target.upstreams", "%q is listed twice", u)
		}
		seen[strings.ToLower(u)] = true
	}
	if !lbPolicies[t.LBPolicy] {
		return optErr("target.lb_policy", "must be one of round_robin, least_conn, first, client_ip_hash")
	}
	if t.HealthPath != "" {
		path, query, _ := strings.Cut(t.HealthPath, "?")
		if err := checkURLPath(path); err != nil {
			return optErr("target.health_path", "%v", err)
		}
		if hasPlaceholderOrControl(query) || strings.ContainsAny(query, " #") {
			return optErr("target.health_path", "must be a path like /health")
		}
	}
	return t.TLS.validate("target.tls")
}

func (h *PathHeaders) empty() bool {
	return len(h.RequestSet) == 0 && len(h.RequestRemove) == 0 && len(h.ResponseSet) == 0 && len(h.ResponseRemove) == 0
}

func (h *PathHeaders) validate() error {
	ho := HeaderOptions{RequestSet: h.RequestSet, RequestRemove: h.RequestRemove, ResponseSet: h.ResponseSet, ResponseRemove: h.ResponseRemove}
	return ho.validate()
}

func (t *PathTransport) validate() error {
	for _, d := range []struct{ field, v string }{
		{"transport.dial_timeout", t.DialTimeout},
		{"transport.read_timeout", t.ReadTimeout},
		{"transport.write_timeout", t.WriteTimeout},
		{"transport.stream_close_delay", t.StreamCloseDelay},
	} {
		if _, err := parseOptDuration(d.field, d.v); err != nil {
			return err
		}
	}
	return nil
}

// pathsSkipVerify reports whether any proxy route dials its own backend over
// HTTPS without checking the certificate.
func pathsSkipVerify(p []PathRoute) bool {
	for _, r := range p {
		if r.Action == ActionProxy && r.Target.Kind == TargetStatic && r.Target.TLS.TLS && r.Target.TLS.SkipVerify {
			return true
		}
	}
	return false
}

// publicPaths returns a copy for API responses: secret header values
// empty with ValueSet, and every list present.
func publicPaths(p []PathRoute) []PathRoute {
	out := make([]PathRoute, len(p))
	for i, r := range p {
		r.Target.Upstreams = orEmpty(r.Target.Upstreams)
		r.Headers = PathHeaders{
			RequestSet:     publicHeaders(r.Headers.RequestSet),
			RequestRemove:  orEmpty(r.Headers.RequestRemove),
			ResponseSet:    publicHeaders(r.Headers.ResponseSet),
			ResponseRemove: orEmpty(r.Headers.ResponseRemove),
		}
		out[i] = r
	}
	return out
}

// maskedPaths hides every header value, for callers without
// proxy.update.
func maskedPaths(p []PathRoute) []PathRoute {
	out := publicPaths(p)
	for i := range out {
		out[i].Headers.RequestSet = maskHeaders(out[i].Headers.RequestSet)
		out[i].Headers.ResponseSet = maskHeaders(out[i].Headers.ResponseSet)
	}
	return out
}

// pathsWithoutSecrets replaces secret header values by maskedValue for
// the config preview.
func pathsWithoutSecrets(p []PathRoute) []PathRoute { return pathsWithSecretsAs(p, maskedValue) }

// pathsWithEmptySecrets clears secret request header values, so kept
// paths are refilled and checked like sent ones.
func pathsWithEmptySecrets(p []PathRoute) []PathRoute { return pathsWithSecretsAs(p, "") }

func pathsWithSecretsAs(p []PathRoute, v string) []PathRoute {
	out := make([]PathRoute, len(p))
	for i, r := range p {
		set := make([]Header, len(r.Headers.RequestSet))
		for j, h := range r.Headers.RequestSet {
			set[j] = h
			if h.Secret {
				set[j].Value = v
			}
		}
		r.Headers.RequestSet = set
		out[i] = r
	}
	return out
}

// pathSecretValues lists the header values of the path routes, so
// operation logs can mask them like the site's (see SecretValues).
func pathSecretValues(p []PathRoute) []string {
	var out []string
	for _, r := range p {
		for _, h := range append(append([]Header{}, r.Headers.RequestSet...), r.Headers.ResponseSet...) {
			out = append(out, h.Value)
		}
	}
	return out
}

func matchKey(m PathMatch) string { return m.Type + " " + strings.ToLower(normalizePath(m.Path)) }

// applyPathSecrets fills secret request header values the client left
// empty from the stored route with the same match. A route whose backend
// gained an address needs the values again, like a site does (see
// applySecrets); siteGained says whether the site's own upstreams
// gained one, for routes to the site's backend.
func applyPathSecrets(p, prev []PathRoute, siteGained bool) error {
	old := map[string]PathRoute{}
	for _, r := range prev {
		old[matchKey(r.Match)] = r
	}
	for i := range p {
		r := &p[i]
		for j := range r.Headers.ResponseSet {
			r.Headers.ResponseSet[j].ValueSet = false
		}
		was, had := old[matchKey(r.Match)]
		for j := range r.Headers.RequestSet {
			h := &r.Headers.RequestSet[j]
			h.ValueSet = false
			if !h.Secret || h.Value != "" {
				continue
			}
			if had {
				for _, ph := range was.Headers.RequestSet {
					if strings.EqualFold(ph.Name, h.Name) {
						h.Value = ph.Value
						break
					}
				}
			}
			if h.Value == "" {
				return &PathError{Index: i, Field: "headers.request_set", Msg: fmt.Sprintf("secret header %s needs a value", h.Name)}
			}
			if pathGainsBackend(*r, was, siteGained) {
				return &PathError{Index: i, Field: "headers.request_set", Msg: fmt.Sprintf("enter the value of secret header %s again; the path now sends it to another backend", h.Name)}
			}
		}
	}
	return nil
}

// pathGainsBackend reports whether r sends requests to an address the
// stored route did not.
func pathGainsBackend(r, was PathRoute, siteGained bool) bool {
	switch {
	case r.Target.Kind != was.Target.Kind:
		return true
	case r.Target.Kind == TargetSite:
		return siteGained
	}
	return addsUpstream(was.Target.Upstreams, r.Target.Upstreams)
}

// changedPaths reports whether two path lists differ, secrets included.
func changedPaths(a, b []PathRoute) bool {
	ja, _ := json.Marshal(canonicalPaths(a))
	jb, _ := json.Marshal(canonicalPaths(b))
	return string(ja) != string(jb)
}

func canonicalPaths(p []PathRoute) []PathRoute {
	out := make([]PathRoute, len(p))
	for i, r := range p {
		r.Target.Upstreams = orEmpty(r.Target.Upstreams)
		r.Headers.RequestSet = append([]Header{}, r.Headers.RequestSet...)
		r.Headers.RequestRemove = orEmpty(r.Headers.RequestRemove)
		r.Headers.ResponseSet = orEmpty(r.Headers.ResponseSet)
		r.Headers.ResponseRemove = orEmpty(r.Headers.ResponseRemove)
		for j := range r.Headers.RequestSet {
			r.Headers.RequestSet[j].ValueSet = false
		}
		out[i] = r
	}
	return out
}
