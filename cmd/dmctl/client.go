package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is dmctl's HTTP wrapper around the Dockmesh REST API.
// Every subcommand that talks to the server creates one via newClient.
type Client struct {
	server       string
	token        string
	refreshToken string
	insecure     bool
	http         *http.Client
}

// authHTTPClient is a thin http.Client used during the login handshake,
// before a full dmctl Client exists. Shares the TLS posture with the
// main client path via the same `--insecure` flag.
func authHTTPClient() *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: flagInsecure}, // #nosec G402
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// readAll slurps a response body without pulling io/ioutil.
func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// newClient resolves credentials and builds an *http.Client with the
// configured TLS posture. Short-ish default timeout because nothing
// dmctl does (besides logs-follow and exec, which use WS) should take
// minutes.
func newClient() (*Client, error) {
	server, token, insecure, err := resolveCredentials()
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, // #nosec G402 — gated by --insecure flag
	}
	cfg, _ := loadConfig()
	refreshTok := ""
	if cfg != nil {
		refreshTok = cfg.RefreshToken
	}
	return &Client{
		server:       strings.TrimRight(server, "/"),
		token:        token,
		refreshToken: refreshTok,
		insecure:     insecure,
		http: &http.Client{
			Transport: tr,
			Timeout:   60 * time.Second,
		},
	}, nil
}

// tryRefresh exchanges the saved refresh_token for a new access_token
// when the server hands us back a 401 on an otherwise valid request.
// On success, the new tokens are persisted to the config file so future
// dmctl invocations start authenticated. Returns true if the refresh
// succeeded and callers should retry the original request.
func (c *Client) tryRefresh() bool {
	if c.refreshToken == "" {
		return false
	}
	body, err := json.Marshal(map[string]string{"refresh_token": c.refreshToken})
	if err != nil {
		return false
	}
	req, err := http.NewRequest("POST", c.server+"/api/v1/auth/refresh", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false
	}
	if out.AccessToken == "" {
		return false
	}
	c.token = out.AccessToken
	if out.RefreshToken != "" {
		c.refreshToken = out.RefreshToken
	}
	// Persist so the next dmctl invocation starts fresh.
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.Token = c.token
	cfg.RefreshToken = c.refreshToken
	_ = saveConfig(cfg)
	return true
}

// request is the one-shot JSON helper. 200-299 are success, anything
// else is turned into a readable error with the response body trimmed
// to keep terminal output sane.
//
// On 401 with a saved refresh_token, we transparently swap in a new
// access token and retry the call once. That's the mechanism that lets
// interactive password logins (15-minute JWTs) survive longer than
// 15 minutes without forcing the user to re-enter credentials.
func (c *Client) request(method, path string, query url.Values, body any, out any) error {
	return c.requestRetry(method, path, query, body, out, true)
}

func (c *Client) requestRetry(method, path string, query url.Values, body any, out any, allowRefresh bool) error {
	u := c.server + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 && allowRefresh && c.tryRefresh() {
		// Retry exactly once with the fresh token — we set allowRefresh
		// to false so a second 401 reports honestly instead of looping.
		return c.requestRetry(method, path, query, body, out, false)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(method, path, resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, trim(string(raw), 200))
	}
	return nil
}

// httpError builds a human-readable error from a non-2xx response,
// preferring the server's JSON `error` field when present.
func httpError(method, path string, status int, body []byte) error {
	var errEnvelope struct {
		Error string `json:"error"`
	}
	msg := string(body)
	if json.Unmarshal(body, &errEnvelope) == nil && errEnvelope.Error != "" {
		msg = errEnvelope.Error
	}
	return fmt.Errorf("%s %s → %d: %s", method, path, status, trim(msg, 400))
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// withHost appends ?host=<id> when the --host flag is set. Stacks and
// container endpoints honour it; others ignore the query param.
func (c *Client) withHost(q url.Values) url.Values {
	if flagHost != "" {
		if q == nil {
			q = url.Values{}
		}
		q.Set("host", flagHost)
	}
	return q
}
