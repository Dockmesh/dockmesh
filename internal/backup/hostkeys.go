package backup

import (
	"context"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// ErrNoPendingHostKey is returned by AcceptHostKey when there is nothing
// to accept.
var ErrNoPendingHostKey = targets.ErrNoPendingHostKey

func jobKeyRecorder(st *store, jobID int64) targets.JSONColumnRecorder {
	return targets.JSONColumnRecorder{DB: st.db, Table: "backup_jobs", Column: "target_config", ID: jobID}
}

func jobTOFU(st *store, n *notices.Service, job *Job) targets.TOFU {
	return targets.TOFU{
		Rec:     jobKeyRecorder(st, job.ID),
		Notices: n,
		ID:      strconv.FormatInt(job.ID, 10),
		Label:   "backup job " + job.Name,
		Link:    "/backups",
	}
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
	jobTOFU(st, n, job).Attach(ctx, t)
	return t, nil
}

// AcceptHostKey trusts the changed host key an SFTP job last saw.
func (s *Service) AcceptHostKey(ctx context.Context, jobID int64) error {
	if _, err := s.store.getJob(ctx, jobID); err != nil {
		return err
	}
	if err := jobKeyRecorder(s.store, jobID).AcceptPendingHostKey(ctx); err != nil {
		return err
	}
	if s.notices != nil {
		return s.notices.Resolve(ctx, targets.ChangedNoticeKey(strconv.FormatInt(jobID, 10)))
	}
	return nil
}
