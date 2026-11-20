package migration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

const (
	// SourceRetention is how long we keep source files + volumes after
	// a successful migration before offering to purge them.
	SourceRetention = 72 * time.Hour
	// CleanupInterval is how often the background cleaner runs.
	CleanupInterval = 1 * time.Hour
)

// StartCleaner launches the background goroutine that marks old
// migrated-away sources for purge notification. Actual purge is
// user-initiated via the API — the cleaner just logs a reminder.
func (s *Service) StartCleaner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanerTick(ctx)
			}
		}
	}()
}

func (s *Service) cleanerTick(ctx context.Context) {
	completed, err := s.store.ListAll(ctx, 500)
	if err != nil {
		return
	}
	now := time.Now()
	for _, m := range completed {
		if m.Status != StatusCompleted || m.CompletedAt == nil {
			continue
		}
		age := now.Sub(*m.CompletedAt)
		if age > SourceRetention {
			slog.Debug("migration cleanup: source past retention",
				"id", m.ID, "stack", m.StackName,
				"age", age.Round(time.Hour))
			// In a full implementation, we'd notify the user or
			// auto-purge. For now, just log.
		}
	}
}

// ErrStackOnSource refuses finalising a migration whose stack runs on the
// source host again.
var ErrStackOnSource = errors.New("the stack runs on the source host again; finalising this migration would stop it")

// PurgeSource finalises a completed migration on the source host: it
// stops what is left of the stack there and removes the volumes the
// migration copied, giving up the rollback. Called via
// DELETE /stacks/{name}/migrate/{id}/source.
func (s *Service) PurgeSource(ctx context.Context, migrationID string) error {
	s.finishMu.Lock()
	m, err := s.store.Get(ctx, migrationID)
	switch {
	case err != nil:
	case m.Status != StatusCompleted:
		err = ErrNotCompleted
	case s.purging[m.ID]:
		err = fmt.Errorf("migration %s is already being finalised", m.ID)
	default:
		s.purging[m.ID] = true
	}
	s.finishMu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		s.finishMu.Lock()
		delete(s.purging, m.ID)
		s.finishMu.Unlock()
	}()
	return s.purgeSource(ctx, m)
}

func (s *Service) purgeSource(ctx context.Context, m *Migration) error {
	release, err := s.locks.TryLock(stacklock.HostKey(m.SourceHostID, m.StackName), stacklock.OpMigration)
	if err != nil {
		return err
	}
	defer release()
	// A later migration may be moving the stack back to the source.
	if active, err := s.store.HasActive(ctx, m.StackName); err != nil {
		return err
	} else if active {
		return ErrMigrationActive
	}
	if s.deployments != nil {
		dep, err := s.deployments.Get(ctx, m.StackName)
		if err != nil {
			return err
		}
		if dep != nil && dep.HostID != m.TargetHostID {
			return ErrStackOnSource
		}
	}

	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		slog.Warn("purge source: host unavailable", "err", err)
		// Not fatal — source may already be decommissioned.
		return nil
	}
	if err := source.StopStack(ctx, m.StackName); err != nil {
		return fmt.Errorf("stop the stack on the source: %w", err)
	}
	p := m.progress()
	if len(p.VolumesCopied) > 0 {
		// Recorded first: once a volume is gone, a rollback would start the
		// source without it.
		p.Purged = true
		if err := s.store.UpdateProgress(ctx, m.ID, p); err != nil {
			return err
		}
	}
	for _, v := range p.VolumesCopied {
		if err := source.RemoveVolume(ctx, v.Name, false); err != nil && !host.VolumeMissing(err) {
			return fmt.Errorf("remove volume %s on the source: %w", v.Name, err)
		}
	}
	slog.Info("migration source purged",
		"id", m.ID, "stack", m.StackName, "source", m.SourceHostID)
	return nil
}
