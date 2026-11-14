package proxy

import "strings"

// caddyConfig is the subset of Caddy's JSON config we generate. Full
// schema: https://caddyserver.com/docs/json/
type caddyConfig struct {
	Admin *caddyAdmin `json:"admin,omitempty"`
	Apps  caddyApps   `json:"apps"`
}

type caddyAdmin struct {
	ID            string   `json:"@id,omitempty"`
	Listen        string   `json:"listen"`
	Origins       []string `json:"origins,omitempty"`
	EnforceOrigin bool     `json:"enforce_origin,omitempty"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
	TLS  *caddyTLS `json:"tls,omitempty"`
}

type caddyHTTP struct {
	Metrics *caddyHTTPMetrics      `json:"metrics,omitempty"`
	Servers map[string]caddyServer `json:"servers"`
}

// caddyHTTPMetrics turns on Caddy's HTTP metrics, which are off unless
// configured; per_host adds the host label the per-site figures use.
type caddyHTTPMetrics struct {
	PerHost bool `json:"per_host"`
}

type caddyServer struct {
	Listen          []string         `json:"listen"`
	Routes          []caddyRoute     `json:"routes"`
	Logs            *caddyServerLogs `json:"logs,omitempty"`
	TrustedProxies  *caddyIPSource   `json:"trusted_proxies,omitempty"`
	ClientIPHeaders []string         `json:"client_ip_headers,omitempty"`
}

type caddyIPSource struct {
	Source string   `json:"source"`
	Ranges []string `json:"ranges"`
}

type caddyServerLogs struct {
	LoggerNames       map[string][]string `json:"logger_names"`
	SkipUnmappedHosts bool                `json:"skip_unmapped_hosts"`
}

type caddyRoute struct {
	Match    []caddyMatch `json:"match,omitempty"`
	Handle   []any        `json:"handle"`
	Terminal bool         `json:"terminal,omitempty"`
}

type caddyMatch struct {
	Host     []string            `json:"host,omitempty"`
	Path     []string            `json:"path,omitempty"`
	Not      []caddyMatch        `json:"not,omitempty"`
	ClientIP *caddyRanges        `json:"client_ip,omitempty"`
	Vars     map[string][]string `json:"vars,omitempty"`
}

type caddyRanges struct {
	Ranges []string `json:"ranges"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

type caddyReverseProxy struct {
	Handler          string                `json:"handler"`
	Upstreams        []caddyUpstream       `json:"upstreams,omitempty"`
	LoadBalancing    *caddyLoadBalancing   `json:"load_balancing,omitempty"`
	HealthChecks     *caddyHealthChecks    `json:"health_checks,omitempty"`
	FlushInterval    int64                 `json:"flush_interval,omitempty"`
	RequestBuffers   int64                 `json:"request_buffers,omitempty"`
	StreamCloseDelay int64                 `json:"stream_close_delay,omitempty"`
	Transport        *caddyTransport       `json:"transport,omitempty"`
	Headers          *caddyHeaderOps       `json:"headers,omitempty"`
	Rewrite          *caddyRewrite         `json:"rewrite,omitempty"`
	HandleResponse   []caddyResponseHandle `json:"handle_response,omitempty"`
}

type caddyLoadBalancing struct {
	SelectionPolicy map[string]string `json:"selection_policy"`
}

type caddyHealthChecks struct {
	Active map[string]string `json:"active"`
}

type caddyTransport struct {
	Protocol     string             `json:"protocol"`
	DialTimeout  int64              `json:"dial_timeout,omitempty"`
	ReadTimeout  int64              `json:"read_timeout,omitempty"`
	WriteTimeout int64              `json:"write_timeout,omitempty"`
	TLS          *caddyTransportTLS `json:"tls,omitempty"`
}

type caddyTransportTLS struct {
	CA                 *caddyCAPool `json:"ca,omitempty"`
	ServerName         string       `json:"server_name,omitempty"`
	InsecureSkipVerify bool         `json:"insecure_skip_verify,omitempty"`
}

type caddyCAPool struct {
	Provider       string   `json:"provider"`
	TrustedCACerts []string `json:"trusted_ca_certs"`
}

type caddyHeaderOps struct {
	Request *caddyHeaderOp `json:"request,omitempty"`
}

type caddyHeaderOp struct {
	Set      map[string][]string `json:"set,omitempty"`
	Delete   []string            `json:"delete,omitempty"`
	Deferred bool                `json:"deferred,omitempty"`
}

type caddyHeadersHandler struct {
	Handler  string         `json:"handler"`
	Request  *caddyHeaderOp `json:"request,omitempty"`
	Response *caddyHeaderOp `json:"response,omitempty"`
}

type caddyRewrite struct {
	Method string `json:"method"`
	URI    string `json:"uri"`
}

type caddyResponseHandle struct {
	Match  map[string][]int `json:"match"`
	Routes []caddyRoute     `json:"routes"`
}

type caddyEncode struct {
	Handler   string              `json:"handler"`
	Encodings map[string]struct{} `json:"encodings"`
	Prefer    []string            `json:"prefer"`
}

type caddyRequestBody struct {
	Handler string `json:"handler"`
	MaxSize int64  `json:"max_size"`
}

type caddyAuthentication struct {
	Handler   string         `json:"handler"`
	Providers caddyProviders `json:"providers"`
}

type caddyProviders struct {
	HTTPBasic caddyHTTPBasic `json:"http_basic"`
}

type caddyHTTPBasic struct {
	Accounts  []caddyAccount    `json:"accounts"`
	Hash      map[string]string `json:"hash"`
	HashCache struct{}          `json:"hash_cache"`
}

type caddyAccount struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type caddyStaticResponse struct {
	Handler    string              `json:"handler"`
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers,omitempty"`
}

type caddyTLS struct {
	Automation caddyAutomation `json:"automation"`
}

type caddyAutomation struct {
	Policies []caddyAutomationPolicy `json:"policies"`
}

type caddyAutomationPolicy struct {
	Subjects []string      `json:"subjects"`
	Issuers  []caddyIssuer `json:"issuers"`
}

type caddyIssuer struct {
	Module string `json:"module"`
	CA     string `json:"ca,omitempty"`
	Email  string `json:"email,omitempty"`
}

const (
	serverHTTPS = "dockmesh"
	serverHTTP  = "dockmesh_http"
	portHTTPS   = "443"
	portHTTP    = "80"
)

// redactedHash replaces basic-auth hashes in the config preview.
const redactedHash = "REDACTED"

// buildConfig turns the DB routes into a complete Caddy JSON config and
// returns the hosts of routes it left out because their host, upstream
// or options are not allowed (rows stored before validation existed).
//
// Plain-HTTP routes (tls_mode none) live in a server that listens only
// on :80, which Caddy never applies automatic HTTPS to. TLS routes live
// in the :443 server; Caddy adds their HTTP->HTTPS redirects to the :80
// server after the host-matched plain routes, so a TLS site with
// allow_http is served by a copy of its routes in the :80 server.
func buildConfig(routes []Route, g Settings) (*caddyConfig, []string) {
	return buildConfigWith(routes, g, false)
}

func buildConfigWith(routes []Route, g Settings, redact bool) (*caddyConfig, []string) {
	cfg := &caddyConfig{
		Admin: &caddyAdmin{Listen: adminListen, Origins: adminOrigins, EnforceOrigin: true},
		Apps:  caddyApps{HTTP: caddyHTTP{Metrics: &caddyHTTPMetrics{PerHost: true}, Servers: map[string]caddyServer{}}},
	}

	// A www redirect never takes a name that is a site of its own.
	taken := map[string]bool{}
	for _, r := range routes {
		if r.Enabled {
			taken[strings.ToLower(r.Host)] = true
		}
	}

	var policies []caddyAutomationPolicy
	var plain, secure []caddyRoute
	plainLogs, secureLogs := map[string][]string{}, map[string][]string{}
	var blocked []string
	for _, r := range routes {
		// Disabled routes stay in the DB but are not pushed to Caddy —
		// traffic stops flowing without losing the host/upstream
		// config. Operators can re-enable in one click.
		if !r.Enabled {
			continue
		}
		if checkRouteHost(r.Host) != nil || checkUpstream(r.Upstream) != nil ||
			r.optionsErr != nil || r.Options.validate(r.Host, r.TLSMode) != nil ||
			r.pathsErr != nil || validatePaths(r.Paths) != nil {
			blocked = append(blocked, r.Host)
			continue
		}
		o := &r.Options
		wwwHost := ""
		if o.Routing.WWWRedirect && !taken["www."+strings.ToLower(r.Host)] {
			wwwHost = "www." + r.Host
		}
		if r.TLSMode == "none" {
			site := siteRoutes(r, wwwHost, portHTTP, redact)
			plain = append(plain, site...)
			if o.Logs.AccessLog {
				plainLogs[r.Host] = []string{""}
			}
			continue
		}
		site := siteRoutes(r, wwwHost, portHTTPS, redact)
		subjects := []string{r.Host}
		if wwwHost != "" {
			subjects = append(subjects, wwwHost)
		}
		issuer := g.acmeIssuer()
		if r.TLSMode == "internal" {
			issuer = caddyIssuer{Module: "internal"}
		}
		policies = append(policies, caddyAutomationPolicy{
			Subjects: subjects,
			Issuers:  []caddyIssuer{issuer},
		})
		secure = append(secure, site...)
		if o.Logs.AccessLog {
			secureLogs[r.Host] = []string{""}
		}
		if o.HTTPS.AllowHTTP {
			plain = append(plain, siteRoutes(r, wwwHost, portHTTP, redact)...)
			if o.Logs.AccessLog {
				plainLogs[r.Host] = []string{""}
			}
		}
	}
	if plain == nil {
		plain = []caddyRoute{}
	}
	cfg.Apps.HTTP.Servers[serverHTTP] = caddyServer{Listen: []string{":80"}, Routes: plain, Logs: serverLogs(plainLogs), TrustedProxies: g.trustedProxies(), ClientIPHeaders: g.clientIPHeaders()}
	if len(secure) > 0 {
		cfg.Apps.HTTP.Servers[serverHTTPS] = caddyServer{Listen: []string{":443"}, Routes: secure, Logs: serverLogs(secureLogs), TrustedProxies: g.trustedProxies(), ClientIPHeaders: g.clientIPHeaders()}
	}
	if len(policies) > 0 {
		cfg.Apps.TLS = &caddyTLS{Automation: caddyAutomation{Policies: policies}}
	}
	return cfg, blocked
}

// serverLogs turns on access logs for the given hosts only; the empty
// logger name is Caddy's default log, i.e. the container's output.
func serverLogs(hosts map[string][]string) *caddyServerLogs {
	if len(hosts) == 0 {
		return nil
	}
	return &caddyServerLogs{LoggerNames: hosts, SkipUnmappedHosts: true}
}

// securityHeaders is the "security headers" preset.
var securityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "SAMEORIGIN",
	"Referrer-Policy":        "strict-origin-when-cross-origin",
}

const hstsValue = "max-age=31536000"

// siteRoutes returns the Caddy routes serving one site, in the order
// they must appear in a server: the IP filter, the www redirect, then
// the site itself. A route without options yields exactly one route
// with one reverse_proxy handler. port is the listener the routes go
// into, for X-Forwarded-Port.
func siteRoutes(r Route, wwwHost, port string, redact bool) []caddyRoute {
	o := &r.Options
	var out []caddyRoute

	if o.Access.IPMode != "" {
		ranges := &caddyRanges{Ranges: o.Access.IPRanges}
		hosts := []string{r.Host}
		if wwwHost != "" {
			hosts = append(hosts, wwwHost)
		}
		m := caddyMatch{Host: hosts}
		if o.Access.IPMode == "allow" {
			m.Not = []caddyMatch{{ClientIP: ranges}}
		} else {
			m.ClientIP = ranges
		}
		out = append(out, caddyRoute{
			Match:    []caddyMatch{m},
			Handle:   []any{caddyStaticResponse{Handler: "static_response", StatusCode: 403}},
			Terminal: true,
		})
	}

	if wwwHost != "" {
		scheme := "https"
		if r.TLSMode == "none" {
			scheme = "http"
		}
		out = append(out, caddyRoute{
			Match: []caddyMatch{{Host: []string{wwwHost}}},
			Handle: []any{caddyStaticResponse{
				Handler:    "static_response",
				StatusCode: 301,
				Headers:    map[string][]string{"Location": {scheme + "://" + r.Host + "{http.request.uri}"}},
			}},
			Terminal: true,
		})
	}

	var handle []any
	if resp := frontResponseOp(r); resp != nil {
		handle = append(handle, caddyHeadersHandler{Handler: "headers", Response: resp})
	}
	if o.Performance.Compression {
		handle = append(handle, caddyEncode{
			Handler:   "encode",
			Encodings: map[string]struct{}{"zstd": {}, "gzip": {}},
			Prefer:    []string{"zstd", "gzip"},
		})
	}
	if o.Performance.MaxBodyBytes > 0 {
		handle = append(handle, caddyRequestBody{Handler: "request_body", MaxSize: o.Performance.MaxBodyBytes})
	}
	if len(o.Access.BasicAuth) > 0 {
		a := caddyAuthentication{Handler: "authentication"}
		a.Providers.HTTPBasic.Hash = map[string]string{"algorithm": "bcrypt"}
		for _, u := range o.Access.BasicAuth {
			pw := u.Hash
			if redact {
				pw = redactedHash
			}
			a.Providers.HTTPBasic.Accounts = append(a.Providers.HTTPBasic.Accounts, caddyAccount{Username: u.Username, Password: pw})
		}
		handle = append(handle, a)
	}
	if o.Access.ForwardAuth != nil {
		handle = append(handle, forwardAuthHandler(o.Access.ForwardAuth))
	}
	if len(r.Paths) > 0 {
		handle = append(handle, pathSubroute(r, port))
	} else {
		handle = append(handle, fallbackHandler(r, port))
	}

	return append(out, caddyRoute{
		Match:    []caddyMatch{{Host: []string{r.Host}}},
		Handle:   handle,
		Terminal: true,
	})
}

// responseHeaderOp is deferred so deletes also catch headers the
// upstream sets and set values replace the upstream's.
func responseHeaderOp(o *SiteOptions, tlsMode string) *caddyHeaderOp {
	set := map[string][]string{}
	if o.Headers.Security {
		for k, v := range securityHeaders {
			set[k] = []string{v}
		}
	}
	if o.HTTPS.HSTS && tlsMode != "none" {
		set["Strict-Transport-Security"] = []string{hstsValue}
	}
	for _, h := range o.Headers.ResponseSet {
		set[h.Name] = []string{h.Value}
	}
	if len(set) == 0 && len(o.Headers.ResponseRemove) == 0 {
		return nil
	}
	op := &caddyHeaderOp{Delete: o.Headers.ResponseRemove, Deferred: true}
	if len(set) > 0 {
		op.Set = set
	}
	return op
}

// fallbackHandler is the site's final handler: its upstreams, a redirect
// or a 404.
func fallbackHandler(r Route, port string) any {
	f := r.Options.Routing.Fallback
	switch f.Mode {
	case FallbackRedirect:
		return caddyStaticResponse{Handler: "static_response", StatusCode: f.Code, Headers: map[string][]string{"Location": {f.To}}}
	case FallbackNotFound:
		return caddyStaticResponse{Handler: "static_response", StatusCode: 404}
	}
	return mainProxy(r, port)
}

func mainProxy(r Route, port string) caddyReverseProxy {
	o := &r.Options
	return siteBackend(r).proxy(requestHeaderOps(o, hostHeader(&o.Routing.UpstreamTLS), port))
}

// backend is what a reverse_proxy handler dials and how.
type backend struct {
	upstreams []string
	lbPolicy  string
	health    string
	tls       *UpstreamTLS
	perf      PerformanceOptions
}

func siteBackend(r Route) backend {
	o := &r.Options
	return backend{
		upstreams: append([]string{r.Upstream}, o.Routing.ExtraUpstreams...),
		lbPolicy:  o.Routing.LBPolicy,
		health:    o.Routing.HealthPath,
		tls:       &o.Routing.UpstreamTLS,
		perf:      o.Performance,
	}
}

func (b backend) proxy(headers *caddyHeaderOps) caddyReverseProxy {
	p := caddyReverseProxy{Handler: "reverse_proxy"}
	for _, u := range b.upstreams {
		p.Upstreams = append(p.Upstreams, caddyUpstream{Dial: u})
	}
	if b.lbPolicy != "" {
		p.LoadBalancing = &caddyLoadBalancing{SelectionPolicy: map[string]string{"policy": b.lbPolicy}}
	}
	if b.health != "" {
		p.HealthChecks = &caddyHealthChecks{Active: map[string]string{"uri": b.health}}
	}
	perf := &b.perf
	if perf.FlushImmediately {
		p.FlushInterval = -1
	}
	if perf.BufferRequests {
		p.RequestBuffers = requestBufferSize
	}
	p.StreamCloseDelay = durationNanos(perf.StreamCloseDelay)
	tls := b.tls.caddyTLS()
	if perf.DialTimeout != "" || perf.ReadTimeout != "" || perf.WriteTimeout != "" || tls != nil {
		p.Transport = &caddyTransport{
			Protocol:     "http",
			DialTimeout:  durationNanos(perf.DialTimeout),
			ReadTimeout:  durationNanos(perf.ReadTimeout),
			WriteTimeout: durationNanos(perf.WriteTimeout),
			TLS:          tls,
		}
	}
	p.Headers = headers
	return p
}

// hostHeader is the Host Dockmesh sets on requests to a backend, "" for
// none. Over TLS Caddy sends the dialed address unless told otherwise,
// so keeping the visitor's host needs an explicit value there.
func hostHeader(t *UpstreamTLS) string {
	switch {
	case t.HostHeader == "upstream":
		return "{http.reverse_proxy.upstream.hostport}"
	case t.TLS:
		return "{http.request.hostport}"
	}
	return ""
}

// requestHeaderOps is what the site sets on requests to its backend:
// host as Host and X-Forwarded-Port when asked for, then the site's own
// headers, which replace either on the same name.
func requestHeaderOps(o *SiteOptions, host, port string) *caddyHeaderOps {
	set := map[string][]string{}
	if host != "" {
		set["Host"] = []string{host}
	}
	if o.Headers.ForwardedPort {
		set["X-Forwarded-Port"] = []string{port}
	}
	for _, h := range o.Headers.RequestSet {
		for k := range set {
			if strings.EqualFold(k, h.Name) {
				delete(set, k)
			}
		}
		set[h.Name] = []string{h.Value}
	}
	if len(set) == 0 && len(o.Headers.RequestRemove) == 0 {
		return nil
	}
	op := &caddyHeaderOp{Delete: o.Headers.RequestRemove}
	if len(set) > 0 {
		op.Set = set
	}
	return &caddyHeaderOps{Request: op}
}

// forwardAuthHandler is what Caddy's forward_auth directive expands to:
// the request goes to the auth service first; on a 2xx answer the
// copied headers are set on the original request (or removed when the
// service did not send them) and it continues to the next handler. Any
// other answer is returned to the client as is.
func forwardAuthHandler(f *ForwardAuth) caddyReverseProxy {
	t, _ := f.target()
	p := caddyReverseProxy{
		Handler:   "reverse_proxy",
		Upstreams: []caddyUpstream{{Dial: t.dial}},
		Rewrite:   &caddyRewrite{Method: "GET", URI: t.uri},
		Headers: &caddyHeaderOps{Request: &caddyHeaderOp{Set: map[string][]string{
			"X-Forwarded-Method": {"{http.request.method}"},
			"X-Forwarded-Uri":    {"{http.request.uri}"},
		}}},
	}
	if t.tls {
		p.Transport = &caddyTransport{Protocol: "http", TLS: &caddyTransportTLS{}}
	}
	routes := []caddyRoute{{Handle: []any{map[string]string{"handler": "vars"}}}}
	for _, h := range f.CopyHeaders {
		ph := "{http.reverse_proxy.header." + h + "}"
		routes = append(routes,
			caddyRoute{Handle: []any{caddyHeadersHandler{Handler: "headers", Request: &caddyHeaderOp{Delete: []string{h}}}}},
			caddyRoute{
				Match:  []caddyMatch{{Not: []caddyMatch{{Vars: map[string][]string{ph: {""}}}}}},
				Handle: []any{caddyHeadersHandler{Handler: "headers", Request: &caddyHeaderOp{Set: map[string][]string{h: {ph}}}}},
			})
	}
	p.HandleResponse = []caddyResponseHandle{{Match: map[string][]int{"status_code": {2}}, Routes: routes}}
	return p
}
