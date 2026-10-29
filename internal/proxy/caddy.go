package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"
)

const (
	adminAddr = "127.0.0.1:2019"
	adminURL  = "http://" + adminAddr
)

// adminOrigins are the only Host/Origin values Caddy's admin API
// accepts. Requests without a matching Origin (browsers on other sites,
// DNS rebinding) are refused; see Service docs for what this does not
// cover.
var adminOrigins = []string{adminAddr, "localhost:2019"}

// caddyConfig is the subset of Caddy's JSON config we generate. Full
// schema: https://caddyserver.com/docs/json/
type caddyConfig struct {
	Admin *caddyAdmin `json:"admin,omitempty"`
	Apps  caddyApps   `json:"apps"`
}

type caddyAdmin struct {
	Listen        string   `json:"listen"`
	Origins       []string `json:"origins,omitempty"`
	EnforceOrigin bool     `json:"enforce_origin,omitempty"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
	TLS  *caddyTLS `json:"tls,omitempty"`
}

type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string     `json:"listen"`
	Routes []caddyRoute `json:"routes"`
}

type caddyRoute struct {
	Match    []caddyMatch   `json:"match"`
	Handle   []caddyHandler `json:"handle"`
	Terminal bool           `json:"terminal,omitempty"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

type caddyHandler struct {
	Handler   string          `json:"handler"`
	Upstreams []caddyUpstream `json:"upstreams,omitempty"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
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
}

const (
	serverHTTPS = "dockmesh"
	serverHTTP  = "dockmesh_http"
)

// buildConfig turns the DB routes into a complete Caddy JSON config and
// returns the hosts of routes it left out because their upstream points
// at Caddy's own admin API.
//
// Plain-HTTP routes (tls_mode none) live in a server that listens only
// on :80, which Caddy never applies automatic HTTPS to. TLS routes live
// in the :443 server; Caddy adds their HTTP->HTTPS redirects to the :80
// server after the host-matched plain routes.
func buildConfig(routes []Route) (*caddyConfig, []string) {
	cfg := &caddyConfig{
		Admin: &caddyAdmin{Listen: adminAddr, Origins: adminOrigins, EnforceOrigin: true},
		Apps:  caddyApps{HTTP: caddyHTTP{Servers: map[string]caddyServer{}}},
	}

	var policies []caddyAutomationPolicy
	var plain, secure []caddyRoute
	var blocked []string
	for _, r := range routes {
		// Disabled routes stay in the DB but are not pushed to Caddy —
		// traffic stops flowing without losing the host/upstream
		// config. Operators can re-enable in one click.
		if !r.Enabled {
			continue
		}
		if targetsAdmin(r.Upstream) {
			blocked = append(blocked, r.Host)
			continue
		}
		route := caddyRoute{
			Match: []caddyMatch{{Host: []string{r.Host}}},
			Handle: []caddyHandler{
				{
					Handler:   "reverse_proxy",
					Upstreams: []caddyUpstream{{Dial: r.Upstream}},
				},
			},
			Terminal: true,
		}
		switch r.TLSMode {
		case "none":
			plain = append(plain, route)
			continue
		case "internal":
			policies = append(policies, caddyAutomationPolicy{
				Subjects: []string{r.Host},
				Issuers:  []caddyIssuer{{Module: "internal"}},
			})
		default:
			policies = append(policies, caddyAutomationPolicy{
				Subjects: []string{r.Host},
				Issuers:  []caddyIssuer{{Module: "acme"}},
			})
		}
		secure = append(secure, route)
	}
	if plain == nil {
		plain = []caddyRoute{}
	}
	cfg.Apps.HTTP.Servers[serverHTTP] = caddyServer{Listen: []string{":80"}, Routes: plain}
	if len(secure) > 0 {
		cfg.Apps.HTTP.Servers[serverHTTPS] = caddyServer{Listen: []string{":443"}, Routes: secure}
	}
	if len(policies) > 0 {
		cfg.Apps.TLS = &caddyTLS{Automation: caddyAutomation{Policies: policies}}
	}
	return cfg, blocked
}

// adminRequest builds a request to Caddy's admin API with the Origin
// header enforce_origin requires.
func adminRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, adminURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", adminURL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// pushConfig POSTs the config to Caddy's admin API /load endpoint.
// Caddy replaces its entire running config atomically and saves it, so
// a restarted container (`--resume`) comes back with the same routes.
func pushConfig(ctx context.Context, cfg *caddyConfig) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	req, err := adminRequest(ctx, http.MethodPost, "/load", bytes.NewReader(body))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("caddy /load %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// runningConfig fetches the config Caddy is currently running.
func runningConfig(ctx context.Context) ([]byte, error) {
	req, err := adminRequest(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caddy /config %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// sameConfig reports whether Caddy's running config (as returned by
// /config/) matches want, ignoring formatting and key order.
func sameConfig(running []byte, want *caddyConfig) bool {
	wantJSON, err := json.Marshal(want)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(running, &a) != nil || json.Unmarshal(wantJSON, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// adminStatus pings the Caddy admin API and reports whether it's reachable.
func adminStatus(ctx context.Context) (bool, string) {
	req, err := adminRequest(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return false, ""
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, ""
	}
	// Caddy exposes its version in the Server header of most responses.
	return true, resp.Header.Get("Server")
}

// ensureAdmin waits up to ~15s for the admin API to come up. Used right
// after starting the caddy container.
func ensureAdmin(ctx context.Context) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := adminStatus(ctx); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("caddy admin API did not become ready")
}
