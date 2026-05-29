package auth

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

// SessionCleanup runs the background hygiene loop for the sessions
// table. Two responsibilities, on the same tick:
//
//  1. Idle-revoke active sessions whose last_seen_at is older than the
//     configured SessionIdleTTLMin window. Catches the "tab open in a
//     forgotten browser, never refreshed" case without waiting for the
//     absolute TTL.
//  2. Hard-delete sessions that have been revoked (or absolutely
//     expired) for longer than the configured retention window. Keeps
//     the table from growing indefinitely — a single user with frequent
//     localStorage churn would otherwise accumulate thousands of dead
//     rows over months.
//
// Started from main.go alongside the other periodic services. Tick
// interval is fixed at 1 hour — fine granularity is unnecessary for
// idle eviction and the hard-delete is purely a housekeeping step.
type SessionCleanup struct {
	db       *sql.DB
	settings SettingsReader
	// HardDeleteAfter is how long a revoked or expired row may linger
	// before it gets DELETE'd. Defaults to 30 days. Configurable so
	// compliance teams can keep audit trails longer if they want.
	HardDeleteAfter time.Duration
	// TickInterval controls how often the cleanup runs. Defaults to 1h.
	// Exposed for tests.
	TickInterval time.Duration

	stop chan struct{}
	wg   sync.WaitGroup
}

func NewSessionCleanup(db *sql.DB, settings SettingsReader) *SessionCleanup {
	return &SessionCleanup{
		db:              db,
		settings:        settings,
		HardDeleteAfter: 30 * 24 * time.Hour,
		TickInterval:    1 * time.Hour,
		stop:            make(chan struct{}),
	}
}

// Start kicks off the loop and returns immediately. The first tick
// fires after one TickInterval — not on startup — so a fast crash-and-
// restart doesn't hammer the DB with cleanup cycles.
func (c *SessionCleanup) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.loop(ctx)
	slog.Info("session cleanup started",
		"interval", c.TickInterval,
		"hard_delete_after", c.HardDeleteAfter)
}

func (c *SessionCleanup) Stop() {
	close(c.stop)
	c.wg.Wait()
}

func (c *SessionCleanup) loop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Tick(ctx)
		}
	}
}

// Tick runs one cleanup pass. Exposed so the admin "run cleanup now"
// action (and tests) can trigger it without waiting for the ticker.
func (c *SessionCleanup) Tick(ctx context.Context) {
	cfg := SignInConfig{SessionIdleTTLMin: 60}
	if c.settings != nil {
		cfg = LoadSignInConfig(c.settings)
	}

	if cfg.SessionIdleTTLMin > 0 {
		cutoff := time.Now().Add(-time.Duration(cfg.SessionIdleTTLMin) * time.Minute)
		// COALESCE: a session that's never been refreshed (last_seen_at
		// IS NULL) still counts from its creation. Otherwise a brand-new
		// session would be immune to idle eviction forever.
		res, err := c.db.ExecContext(ctx, `
			UPDATE sessions
			   SET revoked_at = CURRENT_TIMESTAMP
			 WHERE revoked_at IS NULL
			   AND COALESCE(last_seen_at, created_at) < ?`, cutoff)
		if err != nil {
			slog.Warn("session idle-revoke failed", "err", err)
		} else if n, _ := res.RowsAffected(); n > 0 {
			slog.Info("session idle-revoke", "count", n, "idle_ttl_min", cfg.SessionIdleTTLMin)
		}
	}

	// Hard-delete: any session that's been revoked OR absolutely expired
	// for longer than HardDeleteAfter. Done in one statement.
	cutoff := time.Now().Add(-c.HardDeleteAfter)
	res, err := c.db.ExecContext(ctx, `
		DELETE FROM sessions
		 WHERE (revoked_at IS NOT NULL AND revoked_at < ?)
		    OR (expires_at < ?)`, cutoff, cutoff)
	if err != nil {
		slog.Warn("session hard-delete failed", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("session hard-delete", "count", n, "older_than", c.HardDeleteAfter)
	}
}
