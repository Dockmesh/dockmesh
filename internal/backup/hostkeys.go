package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
	"golang.org/x/crypto/ssh"
)

// ErrNoPendingHostKey is returned by AcceptHostKey when there is nothing
// to accept.
var ErrNoPendingHostKey = errors.New("job has no changed host key waiting for approval")

func hostKeyRecordedKey(jobID int64) string {
	return "backups.sftp-host-key-recorded:" + strconv.FormatInt(jobID, 10)
}

func hostKeyChangedKey(jobID int64) string {
	return "backups.sftp-host-key-changed:" + strconv.FormatInt(jobID, 10)
}

// jobTarget builds the job's target. For SFTP it records the host key on
// first contact and parks a changed key until an admin accepts it, so
// existing jobs keep running after the upgrade without anyone re-saving
// them, while a swapped server is refused.
func jobTarget(ctx context.Context, st *store, n *notices.Service, job *Job) (targets.Target, error) {
	t, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		return nil, err
	}
	if sf, ok := t.(*targets.SFTP); ok {
		sf.OnHostKey(func(ev targets.HostKeyEvent) error {
			return handleHostKey(context.WithoutCancel(ctx), st, n, job, ev)
		})
	}
	return t, nil
}

// handleHostKey stores and announces a host key event. Recording the first
// key is conditional: when two connections race on a job without a key,
// only one key wins and the other connection is treated as a changed key.
func handleHostKey(ctx context.Context, st *store, n *notices.Service, job *Job, ev targets.HostKeyEvent) error {
	if ev.Kind == "recorded" {
		won, stored, err := st.recordHostKey(ctx, job.ID, ev.Key)
		if err != nil {
			return err
		}
		if !won && stored != ev.Key {
			ev.Kind = "changed"
			ev.OldFingerprint = stored
			if old, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(stored)); perr == nil {
				ev.OldFingerprint = ssh.FingerprintSHA256(old)
			}
		}
	}
	var notice error
	switch ev.Kind {
	case "recorded":
		notice = raise(ctx, n, notices.RaiseInput{
			Key:      hostKeyRecordedKey(job.ID),
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    "SFTP host key recorded for backup job " + job.Name,
			Body: fmt.Sprintf("The first connection recorded the server's host key %s. From now on "+
				"the job refuses a server that presents a different key.", ev.Fingerprint),
			Link: "/backups",
		})
	case "matched":
		if n != nil {
			notice = n.Resolve(ctx, hostKeyRecordedKey(job.ID))
		}
	case "changed":
		if err := st.setTargetConfigField(ctx, job.ID, "host_key_pending", ev.Key); err != nil {
			return err
		}
		notice = raise(ctx, n, notices.RaiseInput{
			Key:      hostKeyChangedKey(job.ID),
			Audience: notices.Admins(),
			Severity: notices.SevCritical,
			Title:    "SFTP host key changed for backup job " + job.Name,
			Body: fmt.Sprintf("The server used to present %s and now presents %s, so the job no "+
				"longer runs. If the server was reinstalled or its key rotated on purpose, accept "+
				"the new key on the job; otherwise someone may be intercepting the connection.",
				ev.OldFingerprint, ev.Fingerprint),
			Link: "/backups",
		})
	}
	if notice != nil {
		slog.Warn("sftp host key notice", "job", job.Name, "err", notice)
	}
	if ev.Kind == "changed" {
		return &targets.HostKeyChangedError{Old: ev.OldFingerprint, New: ev.Fingerprint}
	}
	return nil
}

func raise(ctx context.Context, n *notices.Service, in notices.RaiseInput) error {
	if n == nil {
		return nil
	}
	return n.Raise(ctx, in)
}

// AcceptHostKey trusts the changed host key an SFTP job last saw.
func (s *Service) AcceptHostKey(ctx context.Context, jobID int64) error {
	job, err := s.store.getJob(ctx, jobID)
	if err != nil {
		return err
	}
	cfg, err := configMap(job.TargetConfig)
	if err != nil {
		return err
	}
	pending, _ := cfg["host_key_pending"].(string)
	if pending == "" {
		return ErrNoPendingHostKey
	}
	cfg["host_key"] = pending
	delete(cfg, "host_key_pending")
	if err := s.store.setTargetConfig(ctx, jobID, cfg); err != nil {
		return err
	}
	if s.notices != nil {
		return s.notices.Resolve(ctx, hostKeyChangedKey(jobID))
	}
	return nil
}

func configMap(cfg any) (map[string]any, error) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if string(b) != "null" {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// recordHostKey stores key as the job's host key unless one is already
// set. It reports whether this call set it and the key now stored.
func (s *store) recordHostKey(ctx context.Context, jobID int64, key string) (bool, string, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE backup_jobs SET target_config = json_set(target_config, '$.host_key', ?),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND COALESCE(json_extract(target_config, '$.host_key'), '') = ''`, key, jobID)
	if err != nil {
		return false, "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, key, nil
	}
	var stored sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT json_extract(target_config, '$.host_key') FROM backup_jobs WHERE id = ?`, jobID).Scan(&stored)
	return false, stored.String, err
}

func (s *store) setTargetConfigField(ctx context.Context, jobID int64, field, value string) error {
	job, err := s.getJob(ctx, jobID)
	if err != nil {
		return err
	}
	cfg, err := configMap(job.TargetConfig)
	if err != nil {
		return err
	}
	cfg[field] = value
	return s.setTargetConfig(ctx, jobID, cfg)
}

func (s *store) setTargetConfig(ctx context.Context, jobID int64, cfg map[string]any) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE backup_jobs SET target_config = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		string(b), jobID)
	return err
}
