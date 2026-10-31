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

func jobTOFU(st *store, n *notices.Service, job *Job) targets.TOFU {
	return targets.TOFU{
		Rec:     targets.JobKeyRecorder(st.db, job.ID),
		Notices: n,
		ID:      strconv.FormatInt(job.ID, 10),
		Label:   "backup job " + job.Name,
		Link:    "/backups",
	}
}

// AcceptHostKey trusts the changed host key an SFTP job last saw.
func (s *Service) AcceptHostKey(ctx context.Context, jobID int64) error {
	job, err := s.store.getJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.TargetID > 0 {
		return targets.NewTargetStore(s.store.db).AcceptHostKey(ctx, job.TargetID, s.notices)
	}
	if err := targets.JobKeyRecorder(s.store.db, jobID).AcceptPendingHostKey(ctx); err != nil {
		return err
	}
	if s.notices != nil {
		return s.notices.Resolve(ctx, targets.ChangedNoticeKey(strconv.FormatInt(jobID, 10)))
	}
	return nil
}
