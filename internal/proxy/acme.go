// ACME event tailer. Caddy logs every certificate-related action in
// structured JSON ("logger":"tls.obtain", "logger":"tls.renew", …).
// We tail the proxy container's stdout, parse the lines we care about,
// and keep the last 200 in a ring buffer so the UI can render a
// timeline without re-scanning logs every time.
package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

const acmeBufferSize = 200

// ACMEEvent is one row in the timeline shown on the proxy page.
type ACMEEvent struct {
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`        // obtain | renew | failure
	Host       string    `json:"host,omitempty"`
	Issuer     string    `json:"issuer,omitempty"`
	Message    string    `json:"message"`
	Successful bool      `json:"successful"`
}

type acmeBuffer struct {
	mu     sync.Mutex
	events []ACMEEvent
}

func (b *acmeBuffer) push(e ACMEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	if len(b.events) > acmeBufferSize {
		b.events = b.events[len(b.events)-acmeBufferSize:]
	}
}

func (b *acmeBuffer) snapshot() []ACMEEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ACMEEvent, len(b.events))
	copy(out, b.events)
	return out
}

// ACMEEvents returns the most recent ACME events the tailer has seen,
// newest last. Empty when the tailer hasn't matched anything yet (or
// the proxy is disabled).
func (s *Service) ACMEEvents() []ACMEEvent {
	if s.acme == nil {
		return []ACMEEvent{}
	}
	return s.acme.snapshot()
}

// StartACMETailer attaches to the Caddy container's log stream and
// pushes parsed ACME events into the ring buffer. Safe to call from
// main even when the proxy isn't enabled — the retry loop inside
// keeps trying every 5s and silently no-ops when Caddy isn't there
// yet. Closes when ctx is cancelled.
func (s *Service) StartACMETailer(ctx context.Context) {
	if s.docker == nil {
		return
	}
	s.mu.Lock()
	if s.acme == nil {
		s.acme = &acmeBuffer{}
	}
	s.mu.Unlock()
	go s.tailACME(ctx)
}

func (s *Service) tailACME(ctx context.Context) {
	cli := s.docker.Raw()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !s.enabled {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		rc, err := cli.ContainerLogs(ctx, ProxyContainerName, container.LogsOptions{
			ShowStdout: true, ShowStderr: true, Follow: true, Tail: "0",
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		s.consumeACMELogs(ctx, rc)
		_ = rc.Close()
		// Reconnect after the stream ends (proxy restart, etc.)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Service) consumeACMELogs(_ context.Context, rc io.ReadCloser) {
	// Caddy uses stdout/stderr-tagged frames when TTY is off; strip
	// the docker multiplex header with stdcopy.
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, rc)
		_ = pw.Close()
	}()
	scan := bufio.NewScanner(pr)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	for scan.Scan() {
		line := scan.Text()
		ev, ok := parseACMELine(line)
		if !ok {
			continue
		}
		s.acme.push(ev)
	}
	if err := scan.Err(); err != nil && !errors.Is(err, io.EOF) {
		slog.Debug("acme log tailer scanner error", "err", err)
	}
}

// parseACMELine matches the structured JSON lines Caddy emits during
// certificate lifecycle. We intentionally don't parse free-text lines —
// false positives there would litter the timeline.
func parseACMELine(line string) (ACMEEvent, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return ACMEEvent{}, false
	}
	var rec struct {
		Level   string  `json:"level"`
		TS      float64 `json:"ts"`
		Logger  string  `json:"logger"`
		Msg     string  `json:"msg"`
		Ident   string  `json:"identifier"`
		Subject string  `json:"subject"`
		Issuer  string  `json:"issuer"`
		Host    string  `json:"host"`
		Error   string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return ACMEEvent{}, false
	}
	if rec.Logger == "" || !strings.HasPrefix(rec.Logger, "tls.") {
		return ACMEEvent{}, false
	}
	host := rec.Ident
	if host == "" {
		host = rec.Subject
	}
	if host == "" {
		host = rec.Host
	}
	ev := ACMEEvent{
		TS:      time.Unix(int64(rec.TS), 0),
		Message: rec.Msg,
		Host:    host,
		Issuer:  rec.Issuer,
	}
	switch {
	case strings.Contains(rec.Logger, "obtain"):
		ev.Kind = "obtain"
	case strings.Contains(rec.Logger, "renew"):
		ev.Kind = "renew"
	default:
		// Catch-all for tls.* events we don't have a specific kind
		// for (e.g. tls.cache). Drop those — they're noise on the
		// timeline.
		return ACMEEvent{}, false
	}
	if rec.Error != "" || strings.Contains(strings.ToLower(rec.Msg), "fail") {
		ev.Kind = "failure"
		ev.Successful = false
		if rec.Error != "" {
			if rec.Msg != "" {
				ev.Message = rec.Msg + ": " + rec.Error
			} else {
				ev.Message = rec.Error
			}
		}
	} else {
		ev.Successful = strings.Contains(rec.Msg, "obtained") ||
			strings.Contains(rec.Msg, "renewed") ||
			strings.Contains(rec.Msg, "certificate obtained") ||
			strings.Contains(rec.Msg, "certificate renewed")
	}
	return ev, true
}
