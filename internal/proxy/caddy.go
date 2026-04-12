package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const adminURL = "http://127.0.0.1:2019"

// caddyConfig is the subset of Caddy's JSON config we generate. Full
// schema: https://caddyserver.com/docs/json/
type caddyConfig struct {
	Admin *caddyAdmin `json:"admin,omitempty"`
	Apps  caddyApps   `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}

type caddyApps struct {
	HTTP caddyHTTP `json:"http"`
	TLS  *caddyTLS `json:"tls,omitempty"`
}

type caddyHTTP struct {
	Servers map[string]caddyServer `json:"servers"`
}

type caddyServer struct {
	Listen []string      `json:"listen"`
	Routes []caddyRoute  `json:"routes"`
}

type caddyRoute struct {
	Match  []caddyMatch   `json:"match"`
	Handle []caddyHandler `json:"handle"`
	Terminal bool         `json:"terminal,omitempty"`
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

// buildConfig turns the DB routes into a complete Caddy JSON config.
func buildConfig(routes []Route) *caddyConfig {
	cfg := &caddyConfig{
		Admin: &caddyAdmin{Listen: "127.0.0.1:2019"},
		Apps: caddyApps{
			HTTP: caddyHTTP{
				Servers: map[string]caddyServer{
					"dockmesh": {
						Listen: []string{":80", ":443"},
						Routes: []caddyRoute{},
					},
				},
			},
		},
	}

	var policies []caddyAutomationPolicy
	server := cfg.Apps.HTTP.Servers["dockmesh"]
	for _, r := range routes {
		server.Routes = append(server.Routes, caddyRoute{
			Match: []caddyMatch{{Host: []string{r.Host}}},
			Handle: []caddyHandler{
				{
					Handler:   "reverse_proxy",
					Upstreams: []caddyUpstream{{Dial: r.Upstream}},
				},
			},
			Terminal: true,
		})
		switch r.TLSMode {
		case "internal":
			policies = append(policies, caddyAutomationPolicy{
				Subjects: []string{r.Host},
				Issuers:  []caddyIssuer{{Module: "internal"}},
			})
		case "auto":
			policies = append(policies, caddyAutomationPolicy{
				Subjects: []string{r.Host},
				Issuers:  []caddyIssuer{{Module: "acme"}},
			})
		}
	}
	cfg.Apps.HTTP.Servers["dockmesh"] = server
	if len(policies) > 0 {
		cfg.Apps.TLS = &caddyTLS{Automation: caddyAutomation{Policies: policies}}
	}
	return cfg
}

// pushConfig POSTs the current config to Caddy's admin API /load endpoint.
// Caddy replaces its entire running config atomically.
func (s *Service) pushConfig(ctx context.Context, routes []Route) error {
	cfg := buildConfig(routes)
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/load", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy /load %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// adminStatus pings the Caddy admin API and reports whether it's reachable.
func adminStatus(ctx context.Context) (bool, string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/config/", nil)
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
