package setup

import (
	"context"
	"database/sql"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// ForceIgnoredNoticeKey is the notice raised while DOCKMESH_SETUP_FORCE is
// set on an install that already has accounts.
const ForceIgnoredNoticeKey = "setup.force-ignored"

// DefaultEnvFile is where install.sh writes the env file on Linux.
const DefaultEnvFile = "/var/lib/dockmesh/dockmesh.env"

// NewForBoot counts the accounts in db, decides the Mode from f and builds
// the setup state for this server start, with codes read from codeFile
// (empty: CodeFile). forceIgnored reports that
// DOCKMESH_SETUP_FORCE is set but setup mode stays closed: the flag
// outlives reinstalls in the env file, so it never opens setup by itself.
func NewForBoot(ctx context.Context, db *sql.DB, f Facts, codeFile string) (st *State, mode Mode, forceIgnored bool, err error) {
	if db != nil {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&f.Users); err != nil {
			return nil, SetUp, false, err
		}
	}
	mode = Decide(f)
	st = &State{active: mode != SetUp, startedAt: time.Now(), codes: NewCodes(codeFile)}
	return st, mode, f.Force && mode == SetUp, nil
}

// ForceNoticeCheck raises the admin notice when the force flag was ignored
// at this start and resolves it otherwise. envFile is the env file the
// server was started with, empty when unknown.
func ForceNoticeCheck(forceIgnored bool, envFile string) notices.Check {
	var where any = envFile
	if envFile == "" {
		where = i18n.Msg("notice.setup_force.default_env", DefaultEnvFile)
	}
	return func(ctx context.Context, n *notices.Service) error {
		return n.Set(ctx, forceIgnored, notices.RaiseInput{
			Key:      ForceIgnoredNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    i18n.Msg("notice.setup_force.title"),
			Body:     i18n.Msg("notice.setup_force.body", where),
		})
	}
}
