package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// attachTarget makes the input reference a saved target. An inline target
// is saved first, under the job's name, so every job's credentials live
// in exactly one place. prev is the job being updated, if any: its SFTP
// host key carries over while the server stays the same.
func (s *Service) attachTarget(ctx context.Context, in *JobInput, prev *Job) error {
	if in.TargetID > 0 {
		t, err := targets.NewTargetStore(s.store.db).Get(ctx, in.TargetID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTargetNotFound
		}
		if err != nil {
			return err
		}
		in.TargetType, in.TargetConfig = t.Type, map[string]any{}
		return nil
	}
	if _, err := buildTarget(in.TargetType, in.TargetConfig); err != nil {
		return err
	}
	prevType, prevCfg := "", any(nil)
	if prev != nil {
		prevType, prevCfg = prev.TargetType, prev.TargetConfig
		if prev.TargetID > 0 {
			if t, err := targets.NewTargetStore(s.store.db).Get(ctx, prev.TargetID); err == nil {
				prevType, prevCfg = t.Type, t.Config
			}
		}
	}
	cfg := targets.KeepHostKey(prevType, prevCfg, in.TargetType, in.TargetConfig)
	id, err := s.store.insertTarget(ctx, in.Name, in.TargetType, cfg)
	if err != nil {
		return err
	}
	in.TargetID, in.TargetConfig = id, map[string]any{}
	return nil
}

// execer is what the target helpers need from a *sql.DB or *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// insertTarget saves a target under name, or "name (2)", … when taken.
func insertTarget(ctx context.Context, db execer, name, typ string, cfg any) (int64, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return 0, err
	}
	for i := 1; i < 100; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s (%d)", name, i)
		}
		res, err := db.ExecContext(ctx,
			`INSERT INTO backup_targets (name, type, config_json) VALUES (?, ?, ?) ON CONFLICT(name) DO NOTHING`,
			n, typ, string(raw))
		if err != nil {
			return 0, err
		}
		if c, _ := res.RowsAffected(); c == 1 {
			return res.LastInsertId()
		}
	}
	return 0, fmt.Errorf("no free target name for %q", name)
}

func (s *store) insertTarget(ctx context.Context, name, typ string, cfg any) (int64, error) {
	return insertTarget(ctx, s.db, name, typ, cfg)
}

// MigrateInlineTargets moves the target config stored on each job into a
// saved target and points the job at it. Jobs whose configs only differ
// in a recorded SFTP host key share one target, which keeps that key.
// Deleted jobs are included: their runs still need the target. Each job
// moves in its own transaction, so an interrupted pass resumes with the
// jobs not moved yet. It runs after the one-time secret repair, so a
// repaired secret is what lands in the target.
func (s *Service) MigrateInlineTargets(ctx context.Context) (int, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT id, name, target_type, target_config FROM backup_jobs
		WHERE target_id IS NULL OR target_id NOT IN (SELECT id FROM backup_targets)`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id        int64
		name, typ string
		config    string
	}
	var jobs []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.name, &p.typ, &p.config); err != nil {
			rows.Close()
			return 0, err
		}
		jobs = append(jobs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	moved := 0
	for _, j := range jobs {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(j.config), &cfg); err != nil || len(cfg) == 0 {
			slog.Warn("backup: job target config unreadable, left inline", "job", j.name, "err", err)
			continue
		}
		if err := s.store.moveJobTarget(ctx, j.id, j.typ, cfg); err != nil {
			return moved, fmt.Errorf("job %q: %w", j.name, err)
		}
		moved++
	}
	return moved, nil
}

func (s *store) moveJobTarget(ctx context.Context, jobID int64, typ string, cfg map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := findTarget(ctx, tx, typ, cfg)
	if err != nil {
		return err
	}
	if id == 0 {
		if id, err = insertTarget(ctx, tx, migratedTargetName(typ, cfg), typ, cfg); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE backup_jobs SET target_id = ?, target_config = '{}' WHERE id = ?`, id, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// hostKeyFields are recorded by the target itself, not set by the user.
var hostKeyFields = []string{"host_key", "host_key_pending"}

// findTarget returns a saved target with this type and the same settings
// apart from SFTP host keys. A recorded key the target lacks is copied to
// it; targets that recorded a different key don't match.
func findTarget(ctx context.Context, db execer, typ string, cfg map[string]any) (int64, error) {
	want, err := json.Marshal(withoutHostKeys(cfg))
	if err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, config_json FROM backup_targets WHERE type = ? ORDER BY id`, typ)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id  int64
		cfg map[string]any
	}
	var found []candidate
	for rows.Next() {
		var c candidate
		var raw string
		if err := rows.Scan(&c.id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		if json.Unmarshal([]byte(raw), &c.cfg) != nil {
			continue
		}
		if got, err := json.Marshal(withoutHostKeys(c.cfg)); err == nil && string(got) == string(want) {
			found = append(found, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	key, _ := cfg["host_key"].(string)
	for _, c := range found {
		have, _ := c.cfg["host_key"].(string)
		switch {
		case key == "" || have == key:
			return c.id, nil
		case have == "":
			if _, err := db.ExecContext(ctx,
				`UPDATE backup_targets SET config_json = json_set(config_json, '$.host_key', ?) WHERE id = ?`, key, c.id); err != nil {
				return 0, err
			}
			return c.id, nil
		}
	}
	return 0, nil
}

func withoutHostKeys(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	for _, k := range hostKeyFields {
		delete(out, k)
	}
	return out
}

// migratedTargetName names a target moved out of a job after its type and
// the server it talks to, e.g. "sftp-nas.lan (migrated)".
func migratedTargetName(typ string, cfg map[string]any) string {
	str := func(k string) string { s, _ := cfg[k].(string); return s }
	where := ""
	switch typ {
	case "smb", "sftp":
		where = str("host")
	case "s3":
		where = str("endpoint")
		if b := str("bucket"); b != "" {
			where += "/" + b
		}
	case "webdav":
		if u, err := url.Parse(str("url")); err == nil {
			where = u.Host
		}
	case "local":
		where = path.Base(str("path"))
	}
	if where == "" || where == "." || where == "/" {
		return typ + " (migrated)"
	}
	return typ + "-" + where + " (migrated)"
}

// jobTarget builds the job's target. For SFTP it records the host key on
// first contact and parks a changed key until an admin accepts it. Jobs
// the startup migration couldn't move (unreadable config) still run from
// their inline config.
func jobTarget(ctx context.Context, st *store, n *notices.Service, job *Job) (targets.Target, error) {
	if job.TargetID > 0 {
		ts := targets.NewTargetStore(st.db)
		t, err := ts.Get(ctx, job.TargetID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTargetNotFound
		}
		if err != nil {
			return nil, err
		}
		return ts.BuildChecked(ctx, t, n)
	}
	t, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		return nil, err
	}
	jobTOFU(st, n, job).Attach(ctx, t)
	return t, nil
}
