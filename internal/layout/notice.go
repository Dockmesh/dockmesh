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

// CheckoutNoticeKey is the notice raised while the server runs on the
// data of a source run because no config names a data dir.
const CheckoutNoticeKey = "install.checkout-data"

// CheckoutNoticeCheck raises the admin notice while checkoutDir (see
// CheckoutProbe) is the data dir and resolves it once a config names one.
// binary is the server's executable, for the command the notice shows.
func CheckoutNoticeCheck(checkoutDir, binary string) notices.Check {
	body := i18n.Msg("notice.checkout_data.body", checkoutDir, ConfigFile, binary)
	if runtime.GOOS != "linux" {
		body = i18n.Msg("notice.checkout_data.body_other", checkoutDir)
	}
	return func(ctx context.Context, n *notices.Service) error {
		return n.Set(ctx, checkoutDir != "", notices.RaiseInput{
			Key:      CheckoutNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    i18n.Msg("notice.checkout_data.title"),
			Body:     body,
		})
	}
}
