package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
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
		sf.OnHostKey(func(ev targets.HostKeyEvent) {
			handleHostKey(context.WithoutCancel(ctx), st, n, job, ev)
		})
	}
	return t, nil
}

func handleHostKey(ctx context.Context, st *store, n *notices.Service, job *Job, ev targets.HostKeyEvent) {
	var err error
	switch ev.Kind {
	case "recorded":
		err = st.setTargetConfigField(ctx, job.ID, "host_key", ev.Key)
	case "changed":
		err = st.setTargetConfigField(ctx, job.ID, "host_key_pending", ev.Key)
	}
	if err != nil {
		slog.Warn("store sftp host key", "job", job.Name, "err", err)
	}
	if n == nil {
		return
	}
	switch ev.Kind {
	case "recorded":
		err = n.Raise(ctx, notices.RaiseInput{
			Key:      hostKeyRecordedKey(job.ID),
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    "SFTP host key recorded for backup job " + job.Name,
			Body: fmt.Sprintf("The first connection recorded the server's host key %s. From now on "+
				"the job refuses a server that presents a different key.", ev.Fingerprint),
			Link: "/backups",
		})
	case "matched":
		err = n.Resolve(ctx, hostKeyRecordedKey(job.ID))
	case "changed":
		err = n.Raise(ctx, notices.RaiseInput{
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
	if err != nil {
		slog.Warn("sftp host key notice", "job", job.Name, "err", err)
	}
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
