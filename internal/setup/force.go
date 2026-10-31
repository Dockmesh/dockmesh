package setup

import (
	"context"
	"database/sql"

	"github.com/dockmesh/dockmesh/internal/notices"
)

// ForceIgnoredNoticeKey is the notice raised while DOCKMESH_SETUP_FORCE is
// set on an install that already has an admin.
const ForceIgnoredNoticeKey = "setup.force-ignored"

// DefaultEnvFile is where install.sh writes the env file on Linux.
const DefaultEnvFile = "/var/lib/dockmesh/dockmesh.env"

// NewForBoot builds the setup state for this server start. force is
// DOCKMESH_SETUP_FORCE. It only counts while nobody can administer the
// install: the flag outlives reinstalls in the env file, and honouring it
// with an admin present would hand the install to whoever reaches /setup
// first. forceIgnored reports that the flag was set but overruled.
func NewForBoot(ctx context.Context, db *sql.DB, force bool) (st *State, forceIgnored bool, err error) {
	st, err = NewFromDB(ctx, db)
	if err != nil {
		return nil, false, err
	}
	return st, force && !st.Active(), nil
}

// ForceNoticeCheck raises the admin notice when the force flag was ignored
// at this start and resolves it otherwise. envFile is the env file the
// server was started with, empty when unknown.
func ForceNoticeCheck(forceIgnored bool, envFile string) notices.Check {
	where := envFile
	if where == "" {
		where = "the server's env file (by default " + DefaultEnvFile + ")"
	}
	return func(ctx context.Context, n *notices.Service) error {
		return n.Set(ctx, forceIgnored, notices.RaiseInput{
			Key:      ForceIgnoredNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    "Setup flag ignored",
			Body: "DOCKMESH_SETUP_FORCE=true is set, but an admin account already exists, so the " +
				"setup wizard stays closed. Remove the line from " + where + " and restart Dockmesh.",
		})
	}
}
