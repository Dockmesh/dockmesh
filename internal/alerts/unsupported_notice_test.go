package alerts

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dockmesh/dockmesh/internal/notices"
)

type noticeRow struct {
	id         int64
	body       string
	createdAt  string
	resolved   bool
	dismissals int
}

func readUnsupportedNotice(t *testing.T, database *sql.DB) noticeRow {
	t.Helper()
	var r noticeRow
	var resolved sql.NullString
	err := database.QueryRow(`SELECT id, body, created_at, resolved_at FROM notices WHERE key = ?`, unsupportedNoticeKey).
		Scan(&r.id, &r.body, &r.createdAt, &resolved)
	if err != nil {
		t.Fatal(err)
	}
	r.resolved = resolved.Valid
	if err := database.QueryRow(`SELECT COUNT(*) FROM notice_dismissals WHERE notice_id = ?`, r.id).Scan(&r.dismissals); err != nil {
		t.Fatal(err)
	}
	return r
}

// startAlerts is one server start: the engine begins with no agent
// connected and sees the old agents once they are back.
func startAlerts(database *sql.DB, old ...string) {
	s := NewService(database, nil)
	s.SetNotices(notices.NewService(database))
	ctx := context.Background()
	s.reportUnsupported(ctx, nil)
	s.reportUnsupported(ctx, old)
}

func dismissUnsupported(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT OR IGNORE INTO users (id, username, password) VALUES ('admin-1', 'admin-1', 'x')`); err != nil {
		t.Fatal(err)
	}
	admin := notices.Viewer{UserID: "admin-1", Allowed: func(string) bool { return true }}
	if err := notices.NewService(database).Dismiss(context.Background(), admin, readUnsupportedNotice(t, database).id); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedNoticeDismissalSurvivesRestarts(t *testing.T) {
	_, database := newEvalService(t)
	startAlerts(database, "edge-2", "edge-1")
	dismissUnsupported(t, database)
	first := readUnsupportedNotice(t, database)
	if first.resolved || first.dismissals != 1 {
		t.Fatalf("after dismiss: %+v", first)
	}

	for i := range 2 {
		startAlerts(database, "edge-1", "edge-2")
		if got := readUnsupportedNotice(t, database); got != first {
			t.Fatalf("restart %d with the same agents changed the notice: %+v, was %+v", i+1, got, first)
		}
	}

	startAlerts(database, "edge-1")
	changed := readUnsupportedNotice(t, database)
	if changed.resolved || changed.dismissals != 0 || changed.body == first.body {
		t.Fatalf("a changed agent list must show the notice again: %+v", changed)
	}

	s := NewService(database, nil)
	s.SetNotices(notices.NewService(database))
	s.reportUnsupported(context.Background(), []string{"edge-1"})
	s.reportUnsupported(context.Background(), nil)
	if got := readUnsupportedNotice(t, database); !got.resolved {
		t.Fatalf("no old agent left, notice still open: %+v", got)
	}
}

// Servers before stored text keys wrote only the English body; such a row
// must count as the same notice, or the first start after the upgrade
// would clear its dismissals.
func TestUnsupportedNoticeKeepsDismissalOfPlainTextRow(t *testing.T) {
	_, database := newEvalService(t)
	startAlerts(database, "edge-1")
	if _, err := database.Exec(`UPDATE notices SET body_i18n = '' WHERE key = ?`, unsupportedNoticeKey); err != nil {
		t.Fatal(err)
	}
	dismissUnsupported(t, database)
	before := readUnsupportedNotice(t, database)

	for range 2 {
		startAlerts(database, "edge-1")
	}
	if got := readUnsupportedNotice(t, database); got != before {
		t.Fatalf("plain-text row was reopened: %+v, was %+v", got, before)
	}
}
