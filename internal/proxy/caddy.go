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
	adminPort = 2019
	adminAddr = "127.0.0.1:2019"
)

// adminURL is a variable so tests can point it at a fake admin API.
var adminURL = "http://" + adminAddr

// adminOrigins are the only Host/Origin values Caddy's admin API
// accepts. Requests without a matching Origin (browsers on other sites,
// DNS rebinding) are refused; see the package docs for what this does
// not cover.
var adminOrigins = []string{adminAddr, "localhost:2019"}

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
