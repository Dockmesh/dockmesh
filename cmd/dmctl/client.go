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
	server   string
	token    string
	insecure bool
	http     *http.Client
}

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
	return &Client{
		server:   strings.TrimRight(server, "/"),
		token:    token,
		insecure: insecure,
		http: &http.Client{
			Transport: tr,
			Timeout:   60 * time.Second,
		},
	}, nil
}

// request is the one-shot JSON helper. 200-299 are success, anything
// else is turned into a readable error with the response body trimmed
// to keep terminal output sane.
func (c *Client) request(method, path string, query url.Values, body any, out any) error {
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
