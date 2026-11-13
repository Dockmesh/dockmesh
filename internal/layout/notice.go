package layout

import (
	"context"
	"os"
	"runtime"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// ConfigNoticeKey is the notice raised while the server runs from an env
// file that has not moved to ConfigFile yet.
const ConfigNoticeKey = "install.config-location"

// LegacyEnvFile returns envFile when it is a regular file other than
// configFile, i.e. the config the server runs with has not moved yet.
// Empty off Linux, where the config stays where it is.
func LegacyEnvFile(envFile, configFile string) string {
	if runtime.GOOS != "linux" || envFile == "" || envFile == configFile {
		return ""
	}
	fi, err := os.Lstat(envFile)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return envFile
}

// ConfigNoticeCheck raises the admin notice while envFile has not moved
// and resolves it once it has. Nothing is required of the admin: the next
// upgrade run moves the file.
func ConfigNoticeCheck(envFile string) notices.Check {
	return func(ctx context.Context, n *notices.Service) error {
		legacyFile := LegacyEnvFile(envFile, ConfigFile)
		return n.Set(ctx, legacyFile != "", notices.RaiseInput{
			Key:      ConfigNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    i18n.Msg("notice.config_location.title"),
			Body:     i18n.Msg("notice.config_location.body", legacyFile, ConfigDir),
		})
	}
}
