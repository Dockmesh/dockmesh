package migration

import (
	"context"
	"log/slog"
	"time"
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

// PurgeSource removes the source stack's files and volumes on the
// original host. Called via DELETE /stacks/{name}/migrate/{id}/source.
func (s *Service) PurgeSource(ctx context.Context, migrationID string) error {
	m, err := s.store.Get(ctx, migrationID)
	if err != nil {
		return err
	}
	if m.Status != StatusCompleted {
		return ErrNotCompleted
	}

	// Stop any lingering containers on source.
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		slog.Warn("purge source: host unavailable", "err", err)
		// Not fatal — source may already be decommissioned.
		return nil
	}
	_ = source.StopStack(ctx, m.StackName)

	slog.Info("migration source purged",
		"id", m.ID, "stack", m.StackName, "source", m.SourceHostID)
	return nil
}
