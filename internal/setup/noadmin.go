package setup

import (
	"context"
	"database/sql"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// NoAdminNoticeKey is the notice raised while the install has accounts but
// none of them holds an admin role.
const NoAdminNoticeKey = "setup.no-admin"

// RecoveryCommand creates an admin on the server without the web UI.
const RecoveryCommand = "sudo dockmesh admin create --username <name> --role admin"

// NeedsAdmin reports whether the install has accounts but no admin, the
// state an operator recovers with RecoveryCommand.
func NeedsAdmin(ctx context.Context, db *sql.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	users, err := UsersExist(ctx, db)
	if err != nil || !users {
		return false, err
	}
	admin, err := AdminExists(ctx, db)
	return !admin, err
}

// NoAdminNoticeCheck raises the recovery notice while NeedsAdmin holds and
// resolves it otherwise. Nobody holds the admins audience in that state,
// so every signed-in account sees it.
func NoAdminNoticeCheck(db *sql.DB) notices.Check {
	return func(ctx context.Context, n *notices.Service) error {
		need, err := NeedsAdmin(ctx, db)
		if err != nil {
			return err
		}
		return n.Set(ctx, need, notices.RaiseInput{
			Key:      NoAdminNoticeKey,
			Audience: notices.All(),
			Severity: notices.SevWarning,
			Title:    i18n.Msg("notice.setup_no_admin.title"),
			Body:     i18n.Msg("notice.setup_no_admin.body", RecoveryCommand),
		})
	}
}
