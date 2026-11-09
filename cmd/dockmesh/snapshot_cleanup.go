package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/secretsdb"
)

const (
	snapshotRemovedNoticeKey = "db.plaintext-snapshot-removed"
	snapshotKeptNoticeKey    = "db.plaintext-snapshot-kept"
)

// removePlaintextSnapshots deletes the pre-migration snapshots that still
// hold secrets in plaintext once the upgrade is known to be good: every
// migration applied, the file intact and every stored secret sealed and
// readable. The database is compacted afterwards so the plaintext the
// upgrade rewrote doesn't stay in its free pages either. When a check
// fails the snapshots stay as the way back and admins are told why.
// Without secrets encryption the live database holds the same values, so
// the snapshots are left alone.
func removePlaintextSnapshots(ctx context.Context, database *sql.DB, sec *secrets.Service, n *notices.Service) error {
	if sec == nil || !sec.Enabled() {
		return n.Resolve(ctx, snapshotKeptNoticeKey)
	}
	plain, err := plaintextSnapshots(ctx, database)
	if err != nil {
		return err
	}
	if len(plain) == 0 {
		return n.Resolve(ctx, snapshotKeptNoticeKey)
	}
	if err := errors.Join(db.CheckMigrated(ctx, database), secretsdb.Verify(ctx, database, sec)); err != nil {
		slog.Warn("upgrade checks failed, keeping the pre-upgrade database snapshot", "snapshots", plain, "err", err)
		return n.Raise(ctx, keptNotice(plain, i18n.Msg("notice.snapshot_kept.checks_failed", firstErrorLine(err))))
	}
	var removed, left []string
	for _, p := range plain {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing a pre-upgrade database snapshot failed", "path", p, "err", err)
			left = append(left, p)
			continue
		}
		slog.Info("removed a pre-upgrade database snapshot holding plaintext secrets", "path", p)
		removed = append(removed, p)
	}
	if len(removed) > 0 {
		compactAfterRemoval(ctx, database)
		if err := n.Raise(ctx, removedNotice(removed)); err != nil {
			return err
		}
	}
	if len(left) > 0 {
		return n.Raise(ctx, keptNotice(left, i18n.Msg("notice.snapshot_kept.delete_failed")))
	}
	return n.Resolve(ctx, snapshotKeptNoticeKey)
}

// plaintextSnapshots returns the snapshots that hold plaintext secrets.
// An unfinished copy is never a restore point and may hold them, so it
// always counts; a finished one that can't be read is kept.
func plaintextSnapshots(ctx context.Context, database *sql.DB) ([]string, error) {
	snaps, err := db.Snapshots(database)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range snaps {
		if db.IsUnfinishedSnapshot(p) {
			out = append(out, p)
			continue
		}
		plain, err := snapshotHoldsPlaintext(ctx, p)
		if err != nil {
			slog.Warn("reading a pre-upgrade database snapshot failed, keeping it", "path", p, "err", err)
			continue
		}
		if plain {
			out = append(out, p)
		}
	}
	return out, nil
}

func snapshotHoldsPlaintext(ctx context.Context, path string) (bool, error) {
	snap, err := db.OpenSnapshot(path)
	if err != nil {
		return false, err
	}
	defer snap.Close()
	return secretsdb.HoldsPlaintext(ctx, snap)
}

// compactAfterRemoval runs the compaction now if it fits in the time
// background writers allow, and otherwise leaves it to the next start.
func compactAfterRemoval(ctx context.Context, database *sql.DB) {
	if err := db.MarkCompactPending(ctx, database); err != nil {
		slog.Warn("marking the database for compaction", "err", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.CompactIfPending(cctx, database); err != nil {
		slog.Warn("database not compacted yet, retrying on the next start", "err", err)
	}
}

func snapshotNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

func removedNotice(paths []string) notices.RaiseInput {
	return notices.RaiseInput{
		Key:      snapshotRemovedNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevInfo,
		Title:    i18n.Msg("notice.snapshot_removed.title"),
		Body:     i18n.Msg("notice.snapshot_removed.body", notices.Bullets(snapshotNames(paths))),
		Link:     "/backups",
	}
}

func keptNotice(paths []string, reason i18n.Text) notices.RaiseInput {
	return notices.RaiseInput{
		Key:      snapshotKeptNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.snapshot_kept.title"),
		Body:     i18n.Msg("notice.snapshot_kept.body", notices.Bullets(snapshotNames(paths)), reason),
	}
}

func firstErrorLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}
