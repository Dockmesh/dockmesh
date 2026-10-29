package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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

// insertTarget saves a target under name, or "name (2)", … when taken.
func (s *store) insertTarget(ctx context.Context, name, typ string, cfg any) (int64, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return 0, err
	}
	for i := 1; i < 100; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s (%d)", name, i)
		}
		res, err := s.db.ExecContext(ctx,
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

// migrateInlineTargets moves the target config stored on each job into
// backup_targets and points the job at it. Identical configs share one
// target. Deleted jobs are included: their runs still need the target.
// The copy keeps recorded SFTP host keys.
func (s *store) migrateInlineTargets(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		var cfg any
		if err := json.Unmarshal([]byte(j.config), &cfg); err != nil {
			slog.Warn("backup: job target config unreadable, left inline", "job", j.name, "err", err)
			continue
		}
		id, err := s.findTarget(ctx, j.typ, cfg)
		if err != nil {
			return moved, err
		}
		if id == 0 {
			if id, err = s.insertTarget(ctx, j.name, j.typ, cfg); err != nil {
				return moved, err
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE backup_jobs SET target_id = ?, target_config = '{}' WHERE id = ?`, id, j.id); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// findTarget returns a saved target with exactly this type and config.
func (s *store) findTarget(ctx context.Context, typ string, cfg any) (int64, error) {
	want, err := json.Marshal(cfg)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, config_json FROM backup_targets WHERE type = ? ORDER BY id`, typ)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return 0, err
		}
		var have any
		if json.Unmarshal([]byte(raw), &have) != nil {
			continue
		}
		if got, err := json.Marshal(have); err == nil && string(got) == string(want) {
			return id, nil
		}
	}
	return 0, rows.Err()
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

// input returns the job as an update payload.
func (j *Job) input() JobInput {
	return JobInput{
		Name:           j.Name,
		HostID:         j.HostID,
		TargetID:       j.TargetID,
		TargetType:     j.TargetType,
		TargetConfig:   j.TargetConfig,
		Sources:        j.Sources,
		Schedule:       j.Schedule,
		RetentionCount: j.RetentionCount,
		RetentionDays:  j.RetentionDays,
		Encrypt:        j.Encrypt,
		PreHooks:       j.PreHooks,
		PostHooks:      j.PostHooks,
		Enabled:        j.Enabled,
	}
}
