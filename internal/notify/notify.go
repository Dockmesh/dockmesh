// Package notify dispatches alert notifications to configured channels.
// Supports 7 channel types out of the box: generic webhook, ntfy, Discord,
// Slack, Microsoft Teams, Gotify and SMTP email. Each channel implements
// the same Send(Notification) signature so the alerts evaluator can fan
// out without caring about the specific type.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Level controls the visual severity of the notification (icon/color).
type Level string

const (
	LevelInfo     Level = "info"
	LevelWarning  Level = "warning"
	LevelCritical Level = "critical"
)

// Notification is the cross-channel message shape. Channel implementations
// pick the fields they need.
type Notification struct {
	Title     string
	Body      string
	Level     Level
	Container string
	Metric    string
	Value     float64
	Threshold float64
	Time      time.Time
}

// Channel is a configured notification destination (one row in
// notification_channels). Each row is rendered into one of the channel
// implementations below by buildChannel().
type Channel struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
	// MuteSchedule is a JSON-encoded recurring-quiet-hours blob in the
	// same shape as alert_rules.mute_schedule (see internal/alerts/
	// schedule.go). When non-empty and the current time falls inside a
	// range, Send/SendToAll skip the channel silently.
	MuteSchedule string `json:"mute_schedule"`
	// Outcome of the most recent delivery attempt, so a broken channel
	// is visible in the UI instead of only in the server log.
	LastSentAt  *time.Time `json:"last_sent_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// channelImpl is the runtime interface satisfied by webhook.go / ntfy.go / …
type channelImpl interface {
	Send(ctx context.Context, n Notification) error
}

// Service persists channels in the DB and dispatches Notifications.
type Service struct {
	db      *sql.DB
	http    *http.Client
	backoff []time.Duration // waits between delivery attempts; len+1 = attempts
	sending chan struct{}   // bounds concurrent deliveries

	mu    sync.RWMutex
	cache map[int64]*Channel // id → row, kept in sync with DB on writes
}

func NewService(db *sql.DB) *Service {
	return &Service{
		db:      db,
		http:    &http.Client{Timeout: 10 * time.Second},
		backoff: []time.Duration{2 * time.Second, 10 * time.Second},
		sending: make(chan struct{}, maxConcurrentSends),
		cache:   make(map[int64]*Channel),
	}
}

// Reload reads the channels table into the in-memory cache. Called once
// at startup and after every CRUD mutation.
func (s *Service) Reload(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, name, config, enabled, mute_schedule,
		        last_sent_at, last_error, last_error_at, created_at, updated_at
		   FROM notification_channels`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := make(map[int64]*Channel)
	for rows.Next() {
		var c Channel
		var enabled int
		var cfg, muteSched string
		var lastSent, lastErrAt sql.NullTime
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &cfg, &enabled, &muteSched,
			&lastSent, &c.LastError, &lastErrAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return err
		}
		if lastSent.Valid {
			t := lastSent.Time
			c.LastSentAt = &t
		}
		if lastErrAt.Valid {
			t := lastErrAt.Time
			c.LastErrorAt = &t
		}
		c.Enabled = enabled == 1
		c.Config = json.RawMessage(cfg)
		c.MuteSchedule = muteSched
		next[c.ID] = &c
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return rows.Err()
}

// SendTo dispatches n to a single channel by id, once. Returns the
// channel's error so the "Test" button can surface it. Honours the
// channel's mute_schedule.
func (s *Service) SendTo(ctx context.Context, id int64, n Notification) error {
	impl, err := s.prepare(id)
	if err != nil || impl == nil {
		return err
	}
	err = impl.Send(ctx, n)
	s.recordResult(ctx, id, err)
	return err
}

// maxConcurrentSends caps deliveries in flight; each can take ~45 s
// against a slow endpoint (3 attempts plus backoff).
const maxConcurrentSends = 64

var (
	errQueueFull       = errors.New("too many deliveries in flight; notification dropped")
	errChannelNotFound = errors.New("channel not found")
	errChannelDisabled = errors.New("channel disabled")
)

// prepare resolves an enabled channel to its implementation. A nil impl
// with nil error means the channel is muted right now.
func (s *Service) prepare(id int64) (channelImpl, error) {
	s.mu.RLock()
	c, ok := s.cache[id]
	s.mu.RUnlock()
	if !ok {
		return nil, errChannelNotFound
	}
	if !c.Enabled {
		return nil, errChannelDisabled
	}
	if isMutedNow(c.MuteSchedule, time.Now()) {
		return nil, nil
	}
	return buildChannel(c, s.http)
}

// SendToAll fans n out to multiple channels in parallel. Each channel
// gets len(backoff)+1 attempts; the final outcome is stored on the
// channel so one broken destination neither blocks the others nor
// fails silently.
func (s *Service) SendToAll(ctx context.Context, ids []int64, n Notification) {
	for _, id := range ids {
		select {
		case s.sending <- struct{}{}:
		default:
			slog.Warn("notify: too many deliveries in flight, dropping", "channel_id", id)
			s.recordResult(ctx, id, errQueueFull)
			continue
		}
		go func(id int64) {
			defer func() { <-s.sending }()
			if err := s.sendWithRetry(ctx, id, n); err != nil {
				slog.Warn("notify send failed", "channel_id", id, "err", err)
			}
		}(id)
	}
}

func (s *Service) sendWithRetry(ctx context.Context, id int64, n Notification) error {
	impl, err := s.prepare(id)
	if errors.Is(err, errChannelNotFound) || errors.Is(err, errChannelDisabled) {
		return err
	}
	if err != nil {
		s.recordResult(ctx, id, err)
		return err
	}
	if impl == nil {
		return nil
	}
	for attempt := 0; ; attempt++ {
		err = impl.Send(ctx, n)
		if err == nil || attempt >= len(s.backoff) {
			break
		}
		t := time.NewTimer(s.backoff[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			s.recordResult(context.WithoutCancel(ctx), id, err)
			return err
		case <-t.C:
		}
	}
	s.recordResult(ctx, id, err)
	return err
}

// maxErrorLen bounds the stored error; response bodies can be large.
const maxErrorLen = 300

// recordResult persists the delivery outcome. URL errors are reduced to
// their cause because the URL itself may carry a webhook token.
func (s *Service) recordResult(ctx context.Context, id int64, sendErr error) {
	now := time.Now().UTC()
	var dbErr error
	msg := ""
	if sendErr == nil {
		_, dbErr = s.db.ExecContext(ctx,
			`UPDATE notification_channels SET last_sent_at = ? WHERE id = ?`, now, id)
	} else {
		var uerr *url.Error
		if errors.As(sendErr, &uerr) {
			msg = uerr.Op + ": " + uerr.Err.Error()
		} else {
			msg = sendErr.Error()
		}
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen]
		}
		_, dbErr = s.db.ExecContext(ctx,
			`UPDATE notification_channels SET last_error = ?, last_error_at = ? WHERE id = ?`, msg, now, id)
	}
	if dbErr != nil {
		slog.Warn("notify: record delivery result", "channel_id", id, "err", dbErr)
	}
	s.mu.Lock()
	if c, ok := s.cache[id]; ok {
		if sendErr == nil {
			c.LastSentAt = &now
		} else {
			c.LastError = msg
			c.LastErrorAt = &now
		}
	}
	s.mu.Unlock()
}

// Channels returns the in-memory snapshot sorted by id ASC. Stable
// order matters because the UI polls every 10s; an unsorted iteration
// would jumble the channel list between refreshes.
func (s *Service) Channels() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.cache))
	for _, c := range s.cache {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// -----------------------------------------------------------------------------
// CRUD helpers
// -----------------------------------------------------------------------------

type ChannelInput struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Config       json.RawMessage `json:"config"`
	Enabled      bool            `json:"enabled"`
	MuteSchedule string          `json:"mute_schedule,omitempty"`
}

var ErrUnknownType = errors.New("unknown channel type")

func validType(t string) bool {
	switch t {
	case "webhook", "ntfy", "discord", "slack", "teams", "gotify", "email",
		"pagerduty", "pushover":
		return true
	}
	return false
}

func (s *Service) Create(ctx context.Context, in ChannelInput) (*Channel, error) {
	if !validType(in.Type) {
		return nil, ErrUnknownType
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_channels (type, name, config, enabled, mute_schedule)
		VALUES (?, ?, ?, ?, ?)`,
		in.Type, in.Name, string(in.Config), boolInt(in.Enabled), in.MuteSchedule)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.byID(id), nil
}

func (s *Service) Update(ctx context.Context, id int64, in ChannelInput) (*Channel, error) {
	if !validType(in.Type) {
		return nil, ErrUnknownType
	}
	if len(in.Config) == 0 {
		in.Config = json.RawMessage("{}")
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE notification_channels
		SET type = ?, name = ?, config = ?, enabled = ?, mute_schedule = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Type, in.Name, string(in.Config), boolInt(in.Enabled), in.MuteSchedule, id); err != nil {
		return nil, err
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s.byID(id), nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE id = ?`, id); err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) byID(id int64) *Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cache[id]
	if !ok {
		return nil
	}
	cp := *c
	return &cp
}

// buildChannel converts a stored row into a runtime channelImpl.
func buildChannel(c *Channel, hc *http.Client) (channelImpl, error) {
	switch c.Type {
	case "webhook":
		return parseWebhook(c.Config, hc)
	case "ntfy":
		return parseNtfy(c.Config, hc)
	case "discord":
		return parseDiscord(c.Config, hc)
	case "slack":
		return parseSlack(c.Config, hc)
	case "teams":
		return parseTeams(c.Config, hc)
	case "gotify":
		return parseGotify(c.Config, hc)
	case "email":
		return parseEmail(c.Config)
	case "pagerduty":
		return parsePagerDuty(c.Config, hc)
	case "pushover":
		return parsePushover(c.Config, hc)
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownType, c.Type)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
