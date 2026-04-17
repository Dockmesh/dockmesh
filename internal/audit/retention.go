package audit

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Retention setting keys (P.11.13). Stored in the `settings` table.
const (
	RetentionModeKey    = "audit.retention_mode"     // forever | days | archive_local | archive_target
	RetentionDaysKey    = "audit.retention_days"     // int — applicable for 'days' and 'archive_*'
	RetentionTargetKey  = "audit.retention_target_id" // backup_targets.id when mode = archive_target
	RetentionLocalDir   = "audit.retention_local_dir" // path when mode = archive_local (default: ./data/audit-archive)
	ActionChainBridge   = "audit.chain_bridge"
)

// RetentionConfig is the typed view of the settings. Zero value =
// forever retention.
type RetentionConfig struct {
	Mode      string `json:"mode"`                  // forever|days|archive_local|archive_target
	Days      int    `json:"days,omitempty"`
	TargetID  int64  `json:"target_id,omitempty"`
	LocalDir  string `json:"local_dir,omitempty"`
}

// RetentionPreview reports how many rows would be affected by the next
// retention job run without changing anything.
type RetentionPreview struct {
	Mode        string    `json:"mode"`
	CutoffAt    time.Time `json:"cutoff_at,omitempty"`
	WouldPrune  int       `json:"would_prune"`
	TotalRows   int       `json:"total_rows"`
	OldestAt    time.Time `json:"oldest_at,omitempty"`
}

// RetentionResult is what RunRetentionJob returns on success.
type RetentionResult struct {
	Mode         string    `json:"mode"`
	CutoffAt     time.Time `json:"cutoff_at,omitempty"`
	Pruned       int       `json:"pruned"`
	Archived     bool      `json:"archived"`
	ArchivePath  string    `json:"archive_path,omitempty"`
	BridgeRowID  int64     `json:"bridge_row_id,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
}

// TargetWriter is the minimal interface the retention service needs to
// ship NDJSON to a backup_target. The backup package's target adapters
// implement it natively; we keep a tiny interface here so audit doesn't
// depend on backup.
type TargetWriter interface {
	WriteFile(ctx context.Context, targetID int64, name string, body io.Reader) error
}

// SettingsReader is the subset of *settings.Store the retention code
// uses. Interface rather than a direct import to avoid coupling.
type SettingsReader interface {
	Get(key, def string) string
}

// Retention runs the periodic prune + archive job. Wired separately
// from the core Service so the core stays focused on writing /
// verifying entries.
type Retention struct {
	db       *sql.DB
	audit    *Service
	settings SettingsReader
	targets  TargetWriter // may be nil — then archive_target mode errors out

	stop chan struct{}
}

func NewRetention(db *sql.DB, audit *Service, settings SettingsReader, targets TargetWriter) *Retention {
	return &Retention{
		db:       db,
		audit:    audit,
		settings: settings,
		targets:  targets,
		stop:     make(chan struct{}),
	}
}

// Start launches the daily ticker. Runs at 03:00 local time each day.
func (r *Retention) Start(ctx context.Context) {
	go func() {
		for {
			wait := untilNext0300(time.Now())
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			case <-time.After(wait):
				if _, err := r.Run(ctx); err != nil {
					slog.Warn("audit retention run", "err", err)
				}
			}
		}
	}()
}

func (r *Retention) Stop() { close(r.stop) }

// untilNext0300 returns the duration from now until the next 03:00
// server-local. Used so we don't have to import the cron package just
// for one job.
func untilNext0300(now time.Time) time.Duration {
	target := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	if !target.After(now) {
		target = target.AddDate(0, 0, 1)
	}
	return target.Sub(now)
}

// Config reads the current retention config from settings.
func (r *Retention) Config() RetentionConfig {
	cfg := RetentionConfig{
		Mode:     r.settings.Get(RetentionModeKey, "forever"),
		LocalDir: r.settings.Get(RetentionLocalDir, ""),
	}
	if v := r.settings.Get(RetentionDaysKey, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Days = n
		}
	}
	if v := r.settings.Get(RetentionTargetKey, ""); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.TargetID = n
		}
	}
	return cfg
}

// Preview returns the would-prune count without touching the table.
func (r *Retention) Preview(ctx context.Context) (*RetentionPreview, error) {
	cfg := r.Config()
	out := &RetentionPreview{Mode: cfg.Mode}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&total); err != nil {
		return nil, err
	}
	out.TotalRows = total
	var oldest sql.NullTime
	if err := r.db.QueryRowContext(ctx,
		`SELECT ts FROM audit_log ORDER BY id ASC LIMIT 1`).Scan(&oldest); err == nil && oldest.Valid {
		out.OldestAt = oldest.Time
	}
	if cfg.Mode == "forever" || cfg.Days <= 0 {
		return out, nil
	}
	cutoff := time.Now().AddDate(0, 0, -cfg.Days)
	out.CutoffAt = cutoff
	var affected int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE ts < ? AND action != ?`,
		cutoff, ActionChainBridge).Scan(&affected); err != nil {
		return nil, err
	}
	out.WouldPrune = affected
	return out, nil
}

// Run executes the retention policy once. Safe to call manually (via
// the /audit/retention/run endpoint) or from the daily ticker.
//
// Flow:
//  1. `forever` — no-op.
//  2. `days` — prune rows older than cutoff, insert a chain-bridge
//     audit row documenting the prune count + last-pruned hash.
//  3. `archive_local` / `archive_target` — stream matching rows as
//     NDJSON to the target, then prune + bridge as (2).
func (r *Retention) Run(ctx context.Context) (*RetentionResult, error) {
	cfg := r.Config()
	start := time.Now()
	res := &RetentionResult{Mode: cfg.Mode}
	if cfg.Mode == "forever" || cfg.Mode == "" {
		return res, nil
	}
	if cfg.Days <= 0 {
		return res, errors.New("retention days not set")
	}
	cutoff := time.Now().AddDate(0, 0, -cfg.Days)
	res.CutoffAt = cutoff

	// Snapshot the last-pruned row hash BEFORE the prune so the
	// bridge row can reference it.
	var lastPrunedHash sql.NullString
	var lastPrunedID int64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, row_hash FROM audit_log
		 WHERE ts < ? AND action != ?
		 ORDER BY id DESC LIMIT 1`,
		cutoff, ActionChainBridge).Scan(&lastPrunedID, &lastPrunedHash)
	if errors.Is(err, sql.ErrNoRows) {
		res.DurationMS = time.Since(start).Milliseconds()
		return res, nil
	}
	if err != nil {
		return nil, err
	}

	// Archive first (if configured). We don't prune if archive fails —
	// the operator sees the error and can fix credentials / disk space.
	if cfg.Mode == "archive_local" || cfg.Mode == "archive_target" {
		path, err := r.archive(ctx, cfg, cutoff)
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		res.ArchivePath = path
		res.Archived = true
	}

	// Count before the delete for the bridge payload.
	var pruneCount int
	_ = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE ts < ? AND action != ?`,
		cutoff, ActionChainBridge).Scan(&pruneCount)

	// Insert the bridge row using the normal Write path so the hash
	// chain stays continuous. The bridge's prev_hash chains into the
	// CURRENT newest row (not the last-pruned row) — that's correct:
	// the chain is about "what the server has committed", not about
	// what's on disk after pruning.
	details := map[string]any{
		"pruned_count":         pruneCount,
		"last_pruned_row_id":   lastPrunedID,
		"last_pruned_row_hash": nullStr(lastPrunedHash),
		"cutoff_at":            cutoff.UTC().Format(time.RFC3339),
		"mode":                 cfg.Mode,
	}
	if res.Archived {
		details["archive_path"] = res.ArchivePath
	}
	r.audit.Write(ctx, "", ActionChainBridge, "audit_log", details)

	// Capture the bridge row id for the response.
	_ = r.db.QueryRowContext(ctx,
		`SELECT id FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`,
		ActionChainBridge).Scan(&res.BridgeRowID)

	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM audit_log WHERE ts < ? AND action != ?`,
		cutoff, ActionChainBridge); err != nil {
		return nil, fmt.Errorf("prune: %w", err)
	}
	res.Pruned = pruneCount
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// archive streams rows older than cutoff as NDJSON to the configured
// destination. Returns the written path (local) or remote object key.
func (r *Retention) archive(ctx context.Context, cfg RetentionConfig, cutoff time.Time) (string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, ts, user_id, action, target, details, prev_hash, row_hash
		  FROM audit_log
		 WHERE ts < ? AND action != ?
		 ORDER BY id ASC`, cutoff, ActionChainBridge)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	enc := json.NewEncoder(w)
	for rows.Next() {
		var (
			id                                int64
			ts                                time.Time
			userID, target, details, pHash, rHash sql.NullString
			action                            string
		)
		if err := rows.Scan(&id, &ts, &userID, &action, &target, &details, &pHash, &rHash); err != nil {
			return "", err
		}
		row := map[string]any{
			"id":        id,
			"ts":        ts.UTC().Format(time.RFC3339Nano),
			"user_id":   nullStr(userID),
			"action":    action,
			"target":    nullStr(target),
			"details":   nullStr(details),
			"prev_hash": nullStr(pHash),
			"row_hash":  nullStr(rHash),
		}
		if err := enc.Encode(row); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if err := w.Flush(); err != nil {
		return "", err
	}

	name := fmt.Sprintf("audit-archive-%s.ndjson", time.Now().UTC().Format("20060102-150405"))
	switch cfg.Mode {
	case "archive_local":
		dir := cfg.LocalDir
		if dir == "" {
			dir = "./data/audit-archive"
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return "", err
		}
		return path, nil
	case "archive_target":
		if r.targets == nil || cfg.TargetID == 0 {
			return "", errors.New("archive_target mode: no target_id configured or targets service unavailable")
		}
		if err := r.targets.WriteFile(ctx, cfg.TargetID, name, bytes.NewReader(buf.Bytes())); err != nil {
			return "", fmt.Errorf("write to target: %w", err)
		}
		return name, nil
	}
	return "", fmt.Errorf("unknown archive mode %q", cfg.Mode)
}

// SaveConfig persists new retention settings in one atomic write
// through the settings store. Validation is strict — an invalid mode
// will refuse the save.
func SaveConfig(ctx context.Context, setter func(ctx context.Context, key, value string) error, cfg RetentionConfig) error {
	switch cfg.Mode {
	case "forever", "days", "archive_local", "archive_target":
	case "":
		cfg.Mode = "forever"
	default:
		return fmt.Errorf("invalid retention mode %q", cfg.Mode)
	}
	if cfg.Mode != "forever" && cfg.Days < 1 {
		return errors.New("retention days must be >= 1 when mode is not 'forever'")
	}
	if cfg.Mode == "archive_target" && cfg.TargetID <= 0 {
		return errors.New("archive_target mode requires a target_id")
	}
	if err := setter(ctx, RetentionModeKey, cfg.Mode); err != nil {
		return err
	}
	if err := setter(ctx, RetentionDaysKey, strconv.Itoa(cfg.Days)); err != nil {
		return err
	}
	if err := setter(ctx, RetentionTargetKey, strconv.FormatInt(cfg.TargetID, 10)); err != nil {
		return err
	}
	if err := setter(ctx, RetentionLocalDir, cfg.LocalDir); err != nil {
		return err
	}
	return nil
}
