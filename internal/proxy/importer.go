package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// MaxCaddyfileSize caps the Caddyfile text accepted for an import.
const MaxCaddyfileSize = 256 << 10

var (
	// ErrCaddyfileRejected is returned for Caddyfiles that are refused
	// before they reach Caddy.
	ErrCaddyfileRejected = errors.New("caddyfile not accepted")
	// ErrCaddyfileInvalid wraps Caddy's own adapt error.
	ErrCaddyfileInvalid = errors.New("caddyfile invalid")
)

// Import statuses of one site.
const (
	ImportReady   = "ready"   // every directive maps to an option
	ImportPartial = "partial" // importable, see Unsupported
	ImportSkipped = "skipped" // cannot be imported, see Error
)

// ImportSite is one site found in a Caddyfile and what became of it.
type ImportSite struct {
	Host        string      `json:"host"`
	Upstream    string      `json:"upstream"`
	TLSMode     string      `json:"tls_mode"`
	Options     SiteOptions `json:"options"`
	Status      string      `json:"status"`
	Unsupported []string    `json:"unsupported"`
	Error       string      `json:"error,omitempty"`
	// RouteID is set when the site was created (disabled).
	RouteID int64 `json:"route_id,omitempty"`
}

// ImportResult lists the sites of a Caddyfile plus what applies to the
// whole file and is not imported.
type ImportResult struct {
	Sites    []ImportSite `json:"sites"`
	Global   []string     `json:"global_unsupported"`
	Warnings []string     `json:"warnings"`
}

// ImportCaddyfile adapts a Caddyfile with the running Caddy, maps its
// sites to routes with options and, unless dryRun, creates every
// importable site disabled so it can be reviewed before it serves.
func (s *Service) ImportCaddyfile(ctx context.Context, caddyfile string, dryRun bool) (*ImportResult, error) {
	if !s.Enabled() {
		return nil, ErrProxyNotConfigured
	}
	if err := screenCaddyfile(caddyfile); err != nil {
		return nil, err
	}
	adapted, warnings, err := adaptCaddyfile(ctx, caddyfile)
	if err != nil {
		return nil, err
	}
	res := mapAdapted(adapted)
	res.Warnings = warnings

	existing, err := s.listRoutes(ctx)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, r := range existing {
		taken[strings.ToLower(r.Host)] = true
	}
	for i := range res.Sites {
		site := &res.Sites[i]
		h := strings.ToLower(site.Host)
		if site.Status != ImportSkipped && taken[h] {
			site.Status, site.Error = ImportSkipped, "host already has a route"
		}
		if site.Status == ImportSkipped {
			continue
		}
		taken[h] = true
		if dryRun {
			continue
		}
		opts := site.Options
		route, err := s.CreateRoute(ctx, RouteSpec{
			Host: site.Host, Upstream: site.Upstream, TLSMode: site.TLSMode,
			Options: &opts, Disabled: true, hashed: true,
		})
		if route == nil {
			site.Status, site.Error = ImportSkipped, err.Error()
			continue
		}
		site.RouteID = route.ID
	}
	for i := range res.Sites {
		res.Sites[i].Options = res.Sites[i].Options.Public()
	}
	return res, nil
}

var snippetDefRe = regexp.MustCompile(`(?m)^\s*\(([^()\s]+)\)\s*\{?\s*$`)

// screenCaddyfile refuses what would make Caddy read anything but the
// submitted text while adapting: imports of files or globs (only
// snippets defined in the text are allowed) and {$ENV} substitution.
// Both would let the importing user read files and environment of the
// proxy container, e.g. certificate keys, through the result or errors.
func screenCaddyfile(text string) error {
	if len(text) > MaxCaddyfileSize {
		return fmt.Errorf("%w: larger than %d KiB", ErrCaddyfileRejected, MaxCaddyfileSize>>10)
	}
	if strings.Contains(text, "{$") {
		return fmt.Errorf("%w: environment variables ({$NAME}) are not supported", ErrCaddyfileRejected)
	}
	snippets := map[string]bool{}
	for _, m := range snippetDefRe.FindAllStringSubmatch(text, -1) {
		snippets[m[1]] = true
	}
	fields := strings.Fields(text)
	for i, f := range fields {
		if strings.Trim(f, "\"`{}") != "import" {
			continue
		}
		if i+1 >= len(fields) || !snippets[strings.Trim(fields[i+1], "\"`")] {
			return fmt.Errorf("%w: import is only supported for snippets defined in the same text", ErrCaddyfileRejected)
		}
	}
	return nil
}

// adaptCaddyfile runs the text through Caddy's /adapt admin endpoint,
// which only converts and does not touch the running config.
func adaptCaddyfile(ctx context.Context, text string) (map[string]any, []string, error) {
	req, err := adminRequest(ctx, http.MethodPost, "/adapt", strings.NewReader(text))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "text/caddyfile")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("caddy /adapt %d", resp.StatusCode)
		}
		if resp.StatusCode < 500 {
			return nil, nil, fmt.Errorf("%w: %s", ErrCaddyfileInvalid, e.Error)
		}
		return nil, nil, errors.New(e.Error)
	}
	var out struct {
		Result   map[string]any `json:"result"`
		Warnings []struct {
			Line      int    `json:"line"`
			Directive string `json:"directive"`
			Message   string `json:"message"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, nil, fmt.Errorf("caddy /adapt: %w", err)
	}
	warnings := []string{}
	for _, w := range out.Warnings {
		msg := w.Message
		if w.Directive != "" {
			msg = w.Directive + ": " + msg
		}
		if w.Line > 0 {
			msg = fmt.Sprintf("line %d: %s", w.Line, msg)
		}
		warnings = append(warnings, msg)
	}
	return out.Result, warnings, nil
}

// siteMapper collects the options of one site and what it could not map.
type siteMapper struct {
	opts        SiteOptions
	upstream    string
	unsupported []string
	hasProxy    bool
}

func (m *siteMapper) skip(format string, args ...any) {
	m.unsupported = append(m.unsupported, fmt.Sprintf(format, args...))
}

// mapAdapted turns an adapted Caddy config into import sites.
func mapAdapted(cfg map[string]any) *ImportResult {
	res := &ImportResult{Sites: []ImportSite{}, Global: []string{}, Warnings: []string{}}
	for k := range cfg {
		if k != "apps" {
			res.Global = append(res.Global, "global option "+k)
		}
	}
	apps := obj(cfg["apps"])
	for k := range apps {
		if k != "http" && k != "tls" {
			res.Global = append(res.Global, "app "+k)
		}
	}
	tlsApp := obj(apps["tls"])
	internal, customIssuer := tlsPolicies(tlsApp)
	if customIssuer {
		res.Global = append(res.Global, "certificate email or CA (set them in the proxy settings)")
	}
	for k := range tlsApp {
		if k != "automation" {
			res.Global = append(res.Global, "tls "+k+" (own certificates are not supported)")
		}
	}

	wwwOf := map[string]bool{}
	servers := obj(obj(apps["http"])["servers"])
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		srv := obj(servers[name])
		plainOnly := listensPlainOnly(srv)
		logged := obj(obj(srv["logs"])["logger_names"])
		for k := range srv {
			switch k {
			case "listen", "routes", "logs", "tls_connection_policies", "automatic_https":
			default:
				res.Global = append(res.Global, "server option "+k)
			}
		}
		for _, raw := range arr(srv["routes"]) {
			route := obj(raw)
			hosts, ok := siteHosts(route)
			if !ok {
				res.Global = append(res.Global, "a route without a host matcher")
				continue
			}
			if target, ok := wwwRedirectTarget(route, hosts); ok {
				wwwOf[strings.ToLower(target)] = true
				continue
			}
			m := &siteMapper{}
			m.mapHandlers(arr(route["handle"]))
			tlsMode := "auto"
			if plainOnly {
				tlsMode = "none"
			}
			if len(hosts) > 1 {
				m.skip("several names in one site block; imported as one site per name")
			}
			for _, h := range hosts {
				site := ImportSite{Host: h, Upstream: m.upstream, Options: m.opts, Unsupported: append([]string{}, m.unsupported...)}
				site.Options.Access.BasicAuth = append([]BasicAuthUser(nil), m.opts.Access.BasicAuth...)
				site.TLSMode = tlsMode
				if tlsMode == "auto" && internal[strings.ToLower(h)] {
					site.TLSMode = "internal"
				}
				if _, ok := logged[h]; ok {
					site.Options.Logs.AccessLog = true
				}
				if hasCustomCert(srv, h) {
					site.Unsupported = append(site.Unsupported, "own certificate (tls <cert> <key>); imported with automatic HTTPS")
				}
				if !m.hasProxy {
					site.Status, site.Error = ImportSkipped, "no reverse_proxy to an upstream"
				}
				res.Sites = append(res.Sites, site)
			}
		}
	}

	for i := range res.Sites {
		site := &res.Sites[i]
		if wwwOf[strings.ToLower(site.Host)] {
			site.Options.Routing.WWWRedirect = true
			delete(wwwOf, strings.ToLower(site.Host))
		}
		finishSite(site)
	}
	for target := range wwwOf {
		res.Global = append(res.Global, "www redirect to "+target+", which is not a site in this file")
	}
	sort.Strings(res.Global)
	return res
}

// finishSite validates a mapped site and sets its status.
func finishSite(site *ImportSite) {
	if site.Status == ImportSkipped {
		return
	}
	site.Options.normalize()
	var err error
	if err = checkRouteHost(site.Host); err == nil {
		if err = checkUpstream(site.Upstream); err == nil {
			err = site.Options.validate(site.Host, site.TLSMode)
		}
	}
	switch {
	case err != nil:
		site.Status, site.Error = ImportSkipped, err.Error()
	case len(site.Unsupported) > 0:
		site.Status = ImportPartial
	default:
		site.Status = ImportReady
	}
}

func (m *siteMapper) mapHandlers(handlers []any) {
	// The Caddyfile adapter wraps each site in one subroute.
	if len(handlers) == 1 && str(obj(handlers[0])["handler"]) == "subroute" {
		for _, r := range arr(obj(handlers[0])["routes"]) {
			m.mapRoute(obj(r))
		}
		return
	}
	for _, h := range handlers {
		m.mapHandler(obj(h))
	}
}

func (m *siteMapper) mapRoute(route map[string]any) {
	handlers := arr(route["handle"])
	if match := arr(route["match"]); len(match) > 0 {
		if m.mapIPFilter(match, handlers) {
			return
		}
		m.skip("handlers with a matcher (%s): %s", matcherNames(match), handlerNames(handlers))
		return
	}
	for _, h := range handlers {
		m.mapHandler(obj(h))
	}
}

// mapIPFilter recognises `@x [not] client_ip|remote_ip ...` + `respond @x 403`.
func (m *siteMapper) mapIPFilter(match []any, handlers []any) bool {
	if len(match) != 1 || len(handlers) != 1 {
		return false
	}
	h := obj(handlers[0])
	if str(h["handler"]) != "static_response" || num(h["status_code"]) != 403 || len(h) > 2 {
		return false
	}
	set := obj(match[0])
	if len(set) != 1 {
		return false
	}
	mode := "deny"
	if not := arr(set["not"]); len(not) == 1 {
		mode = "allow"
		set = obj(not[0])
		if len(set) != 1 {
			return false
		}
	}
	var ranges []any
	for _, k := range []string{"client_ip", "remote_ip"} {
		if v, ok := set[k]; ok {
			ranges = arr(obj(v)["ranges"])
		}
	}
	if len(ranges) == 0 || m.opts.Access.IPMode != "" {
		return false
	}
	m.opts.Access.IPMode = mode
	for _, r := range ranges {
		m.opts.Access.IPRanges = append(m.opts.Access.IPRanges, str(r))
	}
	return true
}

func (m *siteMapper) mapHandler(h map[string]any) {
	switch name := str(h["handler"]); name {
	case "vars":
		if len(h) > 1 {
			m.skip("vars / root")
		}
	case "headers":
		m.mapHeaders(h)
	case "encode":
		m.opts.Performance.Compression = true
		enc := obj(h["encodings"])
		_, gz := enc["gzip"]
		_, zs := enc["zstd"]
		if len(enc) != 2 || !gz || !zs {
			m.skip("encode with other encodings than zstd and gzip; imported as zstd gzip")
		}
	case "request_body":
		m.opts.Performance.MaxBodyBytes = int64(num(h["max_size"]))
		if len(h) > 2 {
			m.skip("request_body options other than max_size")
		}
	case "authentication":
		m.mapBasicAuth(h)
	case "reverse_proxy":
		if _, ok := h["handle_response"]; ok {
			m.mapForwardAuth(h)
			return
		}
		m.mapProxy(h)
	default:
		m.skip("%s", handlerLabel(name))
	}
}

func (m *siteMapper) mapHeaders(h map[string]any) {
	for k := range h {
		if k != "handler" && k != "request" && k != "response" {
			m.skip("header option %s", k)
		}
	}
	if req := obj(h["request"]); req != nil {
		set, del := m.headerOps(req, "header (request)")
		m.opts.Headers.RequestSet = append(m.opts.Headers.RequestSet, set...)
		m.opts.Headers.RequestRemove = append(m.opts.Headers.RequestRemove, del...)
	}
	resp := obj(h["response"])
	if resp == nil {
		return
	}
	set, del := m.headerOps(resp, "header")
	m.opts.Headers.ResponseRemove = append(m.opts.Headers.ResponseRemove, del...)
	have := map[string]string{}
	for _, s := range set {
		have[s.Name] = s.Value
	}
	security := true
	for k, v := range securityHeaders {
		if have[k] != v {
			security = false
		}
	}
	if security {
		m.opts.Headers.Security = true
		for k := range securityHeaders {
			delete(have, k)
		}
	}
	if v, ok := have["Strict-Transport-Security"]; ok {
		m.opts.HTTPS.HSTS = true
		if v != hstsValue {
			m.skip("Strict-Transport-Security %q; imported as %q", v, hstsValue)
		}
		delete(have, "Strict-Transport-Security")
	}
	for _, s := range set {
		if _, ok := have[s.Name]; ok {
			m.opts.Headers.ResponseSet = append(m.opts.Headers.ResponseSet, s)
		}
	}
}

// headerOps reads set/delete of a header operation; add, replace and
// placeholder values are reported instead.
func (m *siteMapper) headerOps(op map[string]any, label string) ([]Header, []string) {
	var set []Header
	var del []string
	for k := range op {
		switch k {
		case "set", "delete", "deferred":
		default:
			m.skip("%s %s", label, k)
		}
	}
	setMap := obj(op["set"])
	keys := make([]string, 0, len(setMap))
	for k := range setMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := arr(setMap[k])
		if len(vals) != 1 || hasPlaceholderOrControl(str(vals[0])) {
			m.skip("%s %s with a placeholder or several values", label, k)
			continue
		}
		set = append(set, Header{Name: k, Value: str(vals[0])})
	}
	for _, d := range arr(op["delete"]) {
		del = append(del, str(d))
	}
	return set, del
}

func (m *siteMapper) mapBasicAuth(h map[string]any) {
	basic := obj(obj(h["providers"])["http_basic"])
	if basic == nil || len(obj(h["providers"])) != 1 {
		m.skip("authentication other than basic_auth")
		return
	}
	if alg := str(obj(basic["hash"])["algorithm"]); alg != "" && alg != "bcrypt" {
		m.skip("basic_auth with %s hashes (only bcrypt is supported)", alg)
		return
	}
	for _, a := range arr(basic["accounts"]) {
		acc := obj(a)
		user, hash := str(acc["username"]), str(acc["password"])
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			m.skip("basic_auth user %s: password hash is not a bcrypt hash", user)
			continue
		}
		m.opts.Access.BasicAuth = append(m.opts.Access.BasicAuth, BasicAuthUser{Username: user, Hash: hash})
	}
}

var copyHeaderRe = regexp.MustCompile(`^\{http\.reverse_proxy\.header\.([^}]+)\}$`)

// mapForwardAuth recognises the expansion of Caddy's forward_auth.
func (m *siteMapper) mapForwardAuth(h map[string]any) {
	ups := arr(h["upstreams"])
	rw := obj(h["rewrite"])
	if len(ups) != 1 || rw == nil || m.opts.Access.ForwardAuth != nil {
		m.skip("reverse_proxy with handle_response")
		return
	}
	scheme := "http"
	if obj(obj(h["transport"])["tls"]) != nil {
		scheme = "https"
	}
	fa := &ForwardAuth{URL: scheme + "://" + str(obj(ups[0])["dial"]) + str(rw["uri"])}
	for _, hr := range arr(h["handle_response"]) {
		for _, r := range arr(obj(hr)["routes"]) {
			for _, hd := range arr(obj(r)["handle"]) {
				for _, vals := range obj(obj(obj(hd)["request"])["set"]) {
					if v := arr(vals); len(v) == 1 {
						if mm := copyHeaderRe.FindStringSubmatch(str(v[0])); mm != nil {
							fa.CopyHeaders = append(fa.CopyHeaders, mm[1])
						}
					}
				}
			}
		}
	}
	m.opts.Access.ForwardAuth = fa
}

var knownProxyKeys = map[string]bool{
	"handler": true, "upstreams": true, "load_balancing": true, "health_checks": true,
	"flush_interval": true, "request_buffers": true, "stream_close_delay": true,
	"transport": true, "headers": true,
}

func (m *siteMapper) mapProxy(h map[string]any) {
	if m.hasProxy {
		m.skip("a second reverse_proxy")
		return
	}
	ups := arr(h["upstreams"])
	if len(ups) == 0 {
		m.skip("reverse_proxy without static upstreams")
		return
	}
	m.hasProxy = true
	m.upstream = str(obj(ups[0])["dial"])
	for _, u := range ups[1:] {
		m.opts.Routing.ExtraUpstreams = append(m.opts.Routing.ExtraUpstreams, str(obj(u)["dial"]))
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !knownProxyKeys[k] {
			m.skip("reverse_proxy %s", k)
		}
	}
	if lb := obj(h["load_balancing"]); lb != nil {
		policy := str(obj(lb["selection_policy"])["policy"])
		if lbPolicies[policy] && policy != "" {
			m.opts.Routing.LBPolicy = policy
		} else if policy != "" {
			m.skip("lb_policy %s", policy)
		}
		if len(lb) > 1 {
			m.skip("load balancing options other than lb_policy")
		}
	}
	if hc := obj(h["health_checks"]); hc != nil {
		active := obj(hc["active"])
		m.opts.Routing.HealthPath = str(active["uri"])
		if m.opts.Routing.HealthPath == "" {
			m.opts.Routing.HealthPath = str(active["path"])
		}
		if len(hc) > 1 || len(active) > 1 {
			m.skip("health check options other than health_uri")
		}
	}
	if fi, ok := h["flush_interval"]; ok {
		if num(fi) < 0 {
			m.opts.Performance.FlushImmediately = true
		} else {
			m.skip("flush_interval other than -1")
		}
	}
	if num(h["request_buffers"]) > 0 {
		m.opts.Performance.BufferRequests = true
	}
	if d := num(h["stream_close_delay"]); d > 0 {
		m.opts.Performance.StreamCloseDelay = time.Duration(d).String()
	}
	if tr := obj(h["transport"]); tr != nil {
		for k, v := range tr {
			switch k {
			case "protocol":
				if str(v) != "http" {
					m.skip("transport %s", str(v))
				}
			case "dial_timeout":
				m.opts.Performance.DialTimeout = time.Duration(num(v)).String()
			case "read_timeout":
				m.opts.Performance.ReadTimeout = time.Duration(num(v)).String()
			case "write_timeout":
				m.opts.Performance.WriteTimeout = time.Duration(num(v)).String()
			case "tls":
				m.skip("HTTPS to the upstream (transport tls)")
			default:
				m.skip("transport option %s", k)
			}
		}
	}
	if hd := obj(h["headers"]); hd != nil {
		if req := obj(hd["request"]); req != nil {
			set, del := m.headerOps(req, "header_up")
			m.opts.Headers.RequestSet = append(m.opts.Headers.RequestSet, set...)
			m.opts.Headers.RequestRemove = append(m.opts.Headers.RequestRemove, del...)
		}
		if obj(hd["response"]) != nil {
			m.skip("header_down")
		}
	}
}

// wwwRedirectTarget recognises `www.x { redir https://x{uri} }`.
func wwwRedirectTarget(route map[string]any, hosts []string) (string, bool) {
	if len(hosts) != 1 || !strings.HasPrefix(strings.ToLower(hosts[0]), "www.") {
		return "", false
	}
	target := hosts[0][4:]
	handlers := arr(route["handle"])
	if len(handlers) == 1 && str(obj(handlers[0])["handler"]) == "subroute" {
		routes := arr(obj(handlers[0])["routes"])
		if len(routes) != 1 || len(arr(obj(routes[0])["match"])) > 0 {
			return "", false
		}
		handlers = arr(obj(routes[0])["handle"])
	}
	if len(handlers) != 1 {
		return "", false
	}
	h := obj(handlers[0])
	code := num(h["status_code"])
	if str(h["handler"]) != "static_response" || (code != 301 && code != 308) {
		return "", false
	}
	loc := arr(obj(h["headers"])["Location"])
	if len(loc) != 1 {
		return "", false
	}
	for _, scheme := range []string{"https://", "http://"} {
		if strings.EqualFold(str(loc[0]), scheme+target+"{http.request.uri}") {
			return target, true
		}
	}
	return "", false
}

func siteHosts(route map[string]any) ([]string, bool) {
	match := arr(route["match"])
	if len(match) != 1 {
		return nil, false
	}
	set := obj(match[0])
	hosts := arr(set["host"])
	if len(set) != 1 || len(hosts) == 0 {
		return nil, false
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = str(h)
	}
	return out, true
}

func listensPlainOnly(srv map[string]any) bool {
	listen := arr(srv["listen"])
	if len(listen) == 0 {
		return false
	}
	for _, l := range listen {
		if !strings.HasSuffix(str(l), ":80") {
			return false
		}
	}
	return true
}

// tlsPolicies returns the subjects issued by the internal CA and
// whether any policy sets its own ACME email or CA.
func tlsPolicies(tlsApp map[string]any) (map[string]bool, bool) {
	internal := map[string]bool{}
	custom := false
	for _, p := range arr(obj(tlsApp["automation"])["policies"]) {
		pol := obj(p)
		issuers := arr(pol["issuers"])
		isInternal := len(issuers) > 0
		for _, i := range issuers {
			iss := obj(i)
			if str(iss["module"]) != "internal" {
				isInternal = false
			}
			if len(iss) > 1 {
				custom = true
			}
		}
		if isInternal {
			for _, s := range arr(pol["subjects"]) {
				internal[strings.ToLower(str(s))] = true
			}
		}
	}
	return internal, custom
}

func hasCustomCert(srv map[string]any, host string) bool {
	for _, p := range arr(srv["tls_connection_policies"]) {
		pol := obj(p)
		if pol["certificate_selection"] == nil {
			continue
		}
		for _, sni := range arr(obj(pol["match"])["sni"]) {
			if strings.EqualFold(str(sni), host) {
				return true
			}
		}
	}
	return false
}

// handlerLabel names a Caddy JSON handler by its Caddyfile directive.
func handlerLabel(name string) string {
	switch name {
	case "static_response":
		return "respond / redir"
	case "subroute":
		return "handle / route block"
	case "file_server":
		return "file_server"
	case "rewrite":
		return "rewrite / uri"
	case "":
		return "unknown handler"
	}
	return name
}

func handlerNames(handlers []any) string {
	var names []string
	for _, h := range handlers {
		names = append(names, handlerLabel(str(obj(h)["handler"])))
	}
	return strings.Join(names, ", ")
}

func matcherNames(match []any) string {
	var parts []string
	for _, m := range match {
		set := obj(m)
		keys := make([]string, 0, len(set))
		for k, v := range set {
			if k == "path" {
				var paths []string
				for _, p := range arr(v) {
					paths = append(paths, str(p))
				}
				k = "path " + strings.Join(paths, " ")
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts = append(parts, strings.Join(keys, " and "))
	}
	return strings.Join(parts, "; ")
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func arr(v any) []any {
	a, _ := v.([]any)
	return a
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
