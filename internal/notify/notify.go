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
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// channelImpl is the runtime interface satisfied by webhook.go / ntfy.go / …
type channelImpl interface {
	Send(ctx context.Context, n Notification) error
}

// Service persists channels in the DB and dispatches Notifications.
type Service struct {
	db   *sql.DB
	http *http.Client

	mu    sync.RWMutex
	cache map[int64]*Channel // id → row, kept in sync with DB on writes
}

func NewService(db *sql.DB) *Service {
	return &Service{
		db:    db,
		http:  &http.Client{Timeout: 10 * time.Second},
		cache: make(map[int64]*Channel),
	}
}

// Reload reads the channels table into the in-memory cache. Called once
// at startup and after every CRUD mutation.
func (s *Service) Reload(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, name, config, enabled, created_at, updated_at FROM notification_channels`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := make(map[int64]*Channel)
	for rows.Next() {
		var c Channel
		var enabled int
		var cfg string
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &cfg, &enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return err
		}
		c.Enabled = enabled == 1
		c.Config = json.RawMessage(cfg)
		next[c.ID] = &c
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return rows.Err()
}

// SendTo dispatches n to a single channel by id. Returns the channel
// implementation's error (if any) so handlers can surface it via the
// "Test" button in the UI.
func (s *Service) SendTo(ctx context.Context, id int64, n Notification) error {
	s.mu.RLock()
	c, ok := s.cache[id]
	s.mu.RUnlock()
	if !ok {
		return errors.New("channel not found")
	}
	if !c.Enabled {
		return errors.New("channel disabled")
	}
	impl, err := buildChannel(c, s.http)
	if err != nil {
		return err
	}
	return impl.Send(ctx, n)
}

// SendToAll fans n out to multiple channels in parallel, swallowing
// individual failures (logged) so one broken channel can't block alerts.
func (s *Service) SendToAll(ctx context.Context, ids []int64, n Notification) {
	for _, id := range ids {
		id := id
		go func() {
			if err := s.SendTo(ctx, id, n); err != nil {
				slog.Warn("notify send failed", "channel_id", id, "err", err)
			}
		}()
	}
}

// Channels returns the in-memory snapshot (used by handlers).
func (s *Service) Channels() []Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.cache))
	for _, c := range s.cache {
		out = append(out, *c)
	}
	return out
}

// -----------------------------------------------------------------------------
// CRUD helpers
// -----------------------------------------------------------------------------

type ChannelInput struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Enabled bool            `json:"enabled"`
}

var ErrUnknownType = errors.New("unknown channel type")

func validType(t string) bool {
	switch t {
	case "webhook", "ntfy", "discord", "slack", "teams", "gotify", "email":
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
		INSERT INTO notification_channels (type, name, config, enabled)
		VALUES (?, ?, ?, ?)`,
		in.Type, in.Name, string(in.Config), boolInt(in.Enabled))
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
		SET type = ?, name = ?, config = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Type, in.Name, string(in.Config), boolInt(in.Enabled), id); err != nil {
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
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownType, c.Type)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
