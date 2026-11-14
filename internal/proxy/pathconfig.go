package proxy

import "strings"

type caddySubroute struct {
	Handler string       `json:"handler"`
	Routes  []caddyRoute `json:"routes"`
}

type caddyStripPrefix struct {
	Handler         string `json:"handler"`
	StripPathPrefix string `json:"strip_path_prefix"`
}

// headerSet is a set of header operations keyed case-insensitively.
type headerSet struct {
	set     map[string]string // lower name -> name as written
	val     map[string]string // lower name -> value
	del     []string
	deleted map[string]bool
}

func newHeaderSet() *headerSet {
	return &headerSet{set: map[string]string{}, val: map[string]string{}, deleted: map[string]bool{}}
}

func (h *headerSet) put(name, value string) {
	k := strings.ToLower(name)
	h.set[k], h.val[k] = name, value
	if h.deleted[k] {
		delete(h.deleted, k)
		out := h.del[:0:0]
		for _, d := range h.del {
			if strings.ToLower(d) != k {
				out = append(out, d)
			}
		}
		h.del = out
	}
}

func (h *headerSet) remove(name string) {
	k := strings.ToLower(name)
	delete(h.set, k)
	delete(h.val, k)
	if !h.deleted[k] {
		h.deleted[k] = true
		h.del = append(h.del, name)
	}
}

func (h *headerSet) op(deferred bool) *caddyHeaderOp {
	if len(h.set) == 0 && len(h.del) == 0 {
		return nil
	}
	op := &caddyHeaderOp{Delete: h.del, Deferred: deferred}
	if len(h.set) > 0 {
		op.Set = map[string][]string{}
		for k, name := range h.set {
			op.Set[name] = []string{h.val[k]}
		}
	}
	return op
}

// siteResponseHeaders are the response headers of the site's options:
// the security preset, HSTS, then its own set and remove lists.
func siteResponseHeaders(o *SiteOptions, tlsMode string, only func(string) bool) *headerSet {
	h := newHeaderSet()
	add := func(name, value string) {
		if only(name) {
			h.put(name, value)
		}
	}
	if o.Headers.Security {
		for k, v := range securityHeaders {
			add(k, v)
		}
	}
	if o.HTTPS.HSTS && tlsMode != "none" {
		add("Strict-Transport-Security", hstsValue)
	}
	for _, hd := range o.Headers.ResponseSet {
		add(hd.Name, hd.Value)
	}
	for _, n := range o.Headers.ResponseRemove {
		if only(n) {
			h.remove(n)
		}
	}
	return h
}

// routeResponseNames are the response header names any path route sets
// or removes. Caddy applies the outermost deferred headers handler last,
// so the site's handler in front of the paths would override a route on
// the same name; those names move from it into every path.
func routeResponseNames(p []PathRoute) map[string]bool {
	out := map[string]bool{}
	for _, r := range p {
		for _, h := range r.Headers.ResponseSet {
			out[strings.ToLower(h.Name)] = true
		}
		for _, n := range r.Headers.ResponseRemove {
			out[strings.ToLower(n)] = true
		}
	}
	return out
}

// frontResponseOp is the site's response headers handler in front of
// everything; for a site with path routes it leaves out the names a
// route sets or removes.
func frontResponseOp(r Route) *caddyHeaderOp {
	if len(r.Paths) == 0 {
		return responseHeaderOp(&r.Options, r.TLSMode)
	}
	names := routeResponseNames(r.Paths)
	return siteResponseHeaders(&r.Options, r.TLSMode, func(n string) bool { return !names[strings.ToLower(n)] }).op(true)
}

// pathSubroute replaces the site's final handler when it has path
// routes: the routes in their fixed order, then the fallback.
func pathSubroute(r Route, port string) caddySubroute {
	names := routeResponseNames(r.Paths)
	moved := func(n string) bool { return names[strings.ToLower(n)] }
	sub := caddySubroute{Handler: "subroute"}
	for _, p := range r.Paths {
		var handle []any
		resp := siteResponseHeaders(&r.Options, r.TLSMode, moved)
		for _, h := range p.Headers.ResponseSet {
			resp.put(h.Name, h.Value)
		}
		for _, n := range p.Headers.ResponseRemove {
			resp.remove(n)
		}
		if op := resp.op(true); op != nil {
			handle = append(handle, caddyHeadersHandler{Handler: "headers", Response: op})
		}
		if p.StripPrefix {
			handle = append(handle, caddyStripPrefix{Handler: "rewrite", StripPathPrefix: p.Match.Path})
		}
		handle = append(handle, pathHandler(r, p, port))
		sub.Routes = append(sub.Routes, caddyRoute{
			Match:    []caddyMatch{{Path: matcherPaths(p.Match)}},
			Handle:   handle,
			Terminal: true,
		})
	}
	var fb []any
	if op := siteResponseHeaders(&r.Options, r.TLSMode, moved).op(true); op != nil {
		fb = append(fb, caddyHeadersHandler{Handler: "headers", Response: op})
	}
	sub.Routes = append(sub.Routes, caddyRoute{Handle: append(fb, fallbackHandler(r, port))})
	return sub
}

// matcherPaths: a prefix covers the path itself and everything below it,
// never a longer name (/ords, /ords/x, not /ordsfoo).
func matcherPaths(m PathMatch) []string {
	if m.Type == MatchExact {
		return []string{m.Path}
	}
	return []string{m.Path, m.Path + "/*"}
}

func pathHandler(r Route, p PathRoute, port string) any {
	switch p.Action {
	case ActionRedirect:
		return caddyStaticResponse{Handler: "static_response", StatusCode: p.Code, Headers: map[string][]string{"Location": {p.To}}}
	case ActionRespond:
		return caddyStaticResponse{Handler: "static_response", StatusCode: p.Status}
	}
	b := siteBackend(r)
	if p.Target.Kind == TargetStatic {
		b.upstreams, b.lbPolicy, b.health, b.tls = p.Target.Upstreams, p.Target.LBPolicy, p.Target.HealthPath, &p.Target.TLS
	}
	t := &p.Transport
	for _, o := range []struct {
		dst *string
		v   string
	}{
		{&b.perf.DialTimeout, t.DialTimeout},
		{&b.perf.ReadTimeout, t.ReadTimeout},
		{&b.perf.WriteTimeout, t.WriteTimeout},
		{&b.perf.StreamCloseDelay, t.StreamCloseDelay},
	} {
		if o.v != "" {
			*o.dst = o.v
		}
	}
	if t.FlushImmediately != nil {
		b.perf.FlushImmediately = *t.FlushImmediately
	}
	if t.BufferRequests != nil {
		b.perf.BufferRequests = *t.BufferRequests
	}
	return b.proxy(pathRequestHeaders(&r.Options, p, hostHeader(b.tls), port))
}

// pathRequestHeaders are the site's request headers with the route's on
// top: the route wins on the same name, also over Host and
// X-Forwarded-Port. A secret site header goes only to the site's own
// backend: its value was entered for those addresses, and a static
// target could otherwise hand it to any host an editor names. Other
// backends get the name removed, so a visitor can't send their own.
func pathRequestHeaders(o *SiteOptions, p PathRoute, host, port string) *caddyHeaderOps {
	h := newHeaderSet()
	if host != "" {
		h.put("Host", host)
	}
	if o.Headers.ForwardedPort {
		h.put("X-Forwarded-Port", port)
	}
	for _, hd := range o.Headers.RequestSet {
		if hd.Secret && p.Target.Kind != TargetSite {
			h.remove(hd.Name)
			continue
		}
		h.put(hd.Name, hd.Value)
	}
	for _, n := range o.Headers.RequestRemove {
		h.remove(n)
	}
	for _, hd := range p.Headers.RequestSet {
		h.put(hd.Name, hd.Value)
	}
	for _, n := range p.Headers.RequestRemove {
		h.remove(n)
	}
	op := h.op(false)
	if op == nil {
		return nil
	}
	return &caddyHeaderOps{Request: op}
}
