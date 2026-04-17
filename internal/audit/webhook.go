package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Webhook setting keys (P.11.14).
const (
	WebhookURLKey           = "audit.webhook_url"
	WebhookSecretKey        = "audit.webhook_hmac_secret"
	WebhookFilterKey        = "audit.webhook_filter_actions" // JSON array of action prefixes; empty = all
	WebhookMaxRetries       = 5
	WebhookInitialBackoffMS = 500
	WebhookQueueDepth       = 256
)

// WebhookConfig is the persisted config view.
type WebhookConfig struct {
	URL         string   `json:"url,omitempty"`
	HasSecret   bool     `json:"has_secret"`
	FilterActions []string `json:"filter_actions,omitempty"`
}

// WebhookInput is the write-side shape. Password semantics: empty
// secret + clear=false = keep existing; clear=true wipes.
type WebhookInput struct {
	URL            string   `json:"url"`
	Secret         string   `json:"secret,omitempty"`
	ClearSecret    bool     `json:"clear_secret,omitempty"`
	FilterActions  []string `json:"filter_actions,omitempty"`
}

// Webhook is the dispatcher. The Service posts one entry per request,
// queued via an unbuffered-ish channel so audit writes never block on
// a slow receiver. Dropped events are logged so operators see when
// the queue overflows — use for alerts / SIEM integration, not as a
// guaranteed log sink.
type Webhook struct {
	settings SettingsReader
	setSet   func(ctx context.Context, key, value string) error
	client   *http.Client

	mu     sync.Mutex
	queue  chan pendingWebhook
	stop   chan struct{}
	wg     sync.WaitGroup
	// Cached secret so we don't hit the settings store on the hot path.
	// Refreshed on config change.
	secret string
}

type pendingWebhook struct {
	payload []byte
	action  string
}

func NewWebhook(settings SettingsReader, set func(ctx context.Context, key, value string) error) *Webhook {
	return &Webhook{
		settings: settings,
		setSet:   set,
		client:   &http.Client{Timeout: 10 * time.Second},
		queue:    make(chan pendingWebhook, WebhookQueueDepth),
		stop:     make(chan struct{}),
	}
}

// Start launches the dispatcher goroutine. Idempotent — safe to call
// even when URL is unset; the goroutine just drains the (empty) queue.
func (w *Webhook) Start(ctx context.Context) {
	// Load current secret cache.
	w.mu.Lock()
	w.secret = w.settings.Get(WebhookSecretKey, "")
	w.mu.Unlock()

	w.wg.Add(1)
	go w.dispatchLoop(ctx)
}

func (w *Webhook) Stop() {
	close(w.stop)
	w.wg.Wait()
}

// Dispatch enqueues an audit entry for delivery. Non-blocking: if the
// queue is full we drop the oldest (log it) so the audit write never
// stalls. This is deliberate — webhook is best-effort delivery; the
// audit log itself is the source of truth.
func (w *Webhook) Dispatch(entry WebhookEntry) {
	if w == nil {
		return
	}
	url := w.settings.Get(WebhookURLKey, "")
	if url == "" {
		return
	}
	// Action filter: comma-or-JSON-list of prefixes, empty = all.
	if !w.matchesFilter(entry.Action) {
		return
	}
	body, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("audit webhook marshal", "err", err)
		return
	}
	select {
	case w.queue <- pendingWebhook{payload: body, action: entry.Action}:
	default:
		// Queue full — drop the oldest one and enqueue the new one.
		// Dropping newest would starve fresh events during a backlog.
		select {
		case <-w.queue:
		default:
		}
		w.queue <- pendingWebhook{payload: body, action: entry.Action}
		slog.Warn("audit webhook queue full — dropped oldest", "action", entry.Action)
	}
}

// WebhookEntry is the JSON shape posted to the configured URL. Mirrors
// the audit.Entry struct but uses lowercase / snake_case keys that are
// more webhook-consumer-friendly.
type WebhookEntry struct {
	ID       int64     `json:"id"`
	TS       time.Time `json:"ts"`
	UserID   string    `json:"user_id,omitempty"`
	Username string    `json:"username,omitempty"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Details  string    `json:"details,omitempty"`
	RowHash  string    `json:"row_hash,omitempty"`
}

func (w *Webhook) matchesFilter(action string) bool {
	raw := w.settings.Get(WebhookFilterKey, "")
	if raw == "" {
		return true
	}
	// Accept both JSON array and comma-separated list.
	var list []string
	if strings.TrimSpace(raw)[0] == '[' {
		_ = json.Unmarshal([]byte(raw), &list)
	} else {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				list = append(list, s)
			}
		}
	}
	if len(list) == 0 {
		return true
	}
	for _, prefix := range list {
		// Treat trailing ".*" or "*" as "any action starting with
		// the prefix up to the dot". "stack.*" matches "stack.deploy"
		// but not "stackage.foo".
		if strings.HasSuffix(prefix, ".*") {
			p := strings.TrimSuffix(prefix, ".*")
			if strings.HasPrefix(action, p+".") || action == p {
				return true
			}
			continue
		}
		if strings.HasSuffix(prefix, "*") {
			if strings.HasPrefix(action, strings.TrimSuffix(prefix, "*")) {
				return true
			}
			continue
		}
		if action == prefix {
			return true
		}
	}
	return false
}

func (w *Webhook) dispatchLoop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case pw := <-w.queue:
			w.post(ctx, pw)
		}
	}
}

// post sends one payload with retry-with-backoff. Per the P.11.14
// spec: give up after 5 failures so a broken receiver can't pile
// up requests forever.
func (w *Webhook) post(ctx context.Context, pw pendingWebhook) {
	url := w.settings.Get(WebhookURLKey, "")
	if url == "" {
		return
	}
	w.mu.Lock()
	secret := w.secret
	w.mu.Unlock()

	backoff := time.Duration(WebhookInitialBackoffMS) * time.Millisecond
	for attempt := 1; attempt <= WebhookMaxRetries; attempt++ {
		if err := w.postOnce(ctx, url, secret, pw.payload); err != nil {
			if attempt == WebhookMaxRetries {
				slog.Warn("audit webhook delivery failed permanently",
					"action", pw.action, "attempts", attempt, "err", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}
		return
	}
}

func (w *Webhook) postOnce(ctx context.Context, url, secret string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Dockmesh/audit-webhook")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Audit-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	// 2xx = success; 4xx is also terminal (our payload is wrong, no
	// point retrying); 5xx + network errors are retriable.
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return fmt.Errorf("webhook rejected %d", resp.StatusCode)
	default:
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Config CRUD
// -----------------------------------------------------------------------------

func (w *Webhook) Config() WebhookConfig {
	return WebhookConfig{
		URL:          w.settings.Get(WebhookURLKey, ""),
		HasSecret:    w.settings.Get(WebhookSecretKey, "") != "",
		FilterActions: parseFilter(w.settings.Get(WebhookFilterKey, "")),
	}
}

// SaveConfig validates + persists a new config. On success the cached
// secret is refreshed so the next dispatch uses it.
func (w *Webhook) SaveConfig(ctx context.Context, in WebhookInput) (*WebhookConfig, error) {
	url := strings.TrimSpace(in.URL)
	if url != "" {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return nil, errors.New("url must start with http:// or https://")
		}
	}
	if err := w.setSet(ctx, WebhookURLKey, url); err != nil {
		return nil, err
	}
	// Secret handling: explicit clear wins; then new value; else keep.
	switch {
	case in.ClearSecret:
		if err := w.setSet(ctx, WebhookSecretKey, ""); err != nil {
			return nil, err
		}
		w.mu.Lock()
		w.secret = ""
		w.mu.Unlock()
	case in.Secret != "":
		if err := w.setSet(ctx, WebhookSecretKey, in.Secret); err != nil {
			return nil, err
		}
		w.mu.Lock()
		w.secret = in.Secret
		w.mu.Unlock()
	}
	filterStr := ""
	if len(in.FilterActions) > 0 {
		b, _ := json.Marshal(in.FilterActions)
		filterStr = string(b)
	}
	if err := w.setSet(ctx, WebhookFilterKey, filterStr); err != nil {
		return nil, err
	}
	cfg := w.Config()
	return &cfg, nil
}

func parseFilter(raw string) []string {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw)[0] == '[' {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			return out
		}
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SendTest posts a synthetic entry to the configured URL ignoring the
// filter. Used by the UI "Send test" button so operators can verify
// the receiver before enabling.
func (w *Webhook) SendTest(ctx context.Context) error {
	url := w.settings.Get(WebhookURLKey, "")
	if url == "" {
		return errors.New("webhook url not configured")
	}
	w.mu.Lock()
	secret := w.secret
	w.mu.Unlock()
	entry := WebhookEntry{
		ID:       0,
		TS:       time.Now().UTC(),
		Username: "system",
		Action:   "audit.webhook_test",
		Target:   "dockmesh",
		Details:  `{"note":"if you see this your receiver is wired up correctly"}`,
	}
	body, _ := json.Marshal(entry)
	return w.postOnce(ctx, url, secret, body)
}
