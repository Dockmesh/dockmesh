package layout

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"

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
// That command only succeeds when root can trust the checkout
// (rootTrusted); otherwise the notice suggests DOCKMESH_DATA_DIR.
func CheckoutNoticeCheck(checkoutDir, binary string, rootTrusted bool) notices.Check {
	body := i18n.Msg("notice.checkout_data.body_other", checkoutDir, ShellQuote(checkoutDir))
	if runtime.GOOS == "linux" && rootTrusted {
		body = i18n.Msg("notice.checkout_data.body", checkoutDir, ConfigFile,
			"sudo "+ShellQuote(binary)+" system migrate-layout --checkout "+ShellQuote(checkoutDir))
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

// UnitFallbackNoticeKey is the notice raised while the server runs with
// the unit the installer put back because the service did not start with
// the one written from the template.
const UnitFallbackNoticeKey = "install.unit-fallback"

func restoredUnit(data []byte) bool {
	return bytes.HasPrefix(data, []byte(RestoredUnitMarker+"\n"))
}

// RestoredUnit reports whether the unit at path starts with
// RestoredUnitMarker; a missing unit is not one.
func RestoredUnit(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(RestoredUnitMarker)+1)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return restoredUnit(head[:n]), nil
}

// UnitFallbackNoticeCheck raises the admin notice while unitFile is a unit
// RestoreServerUnit put back and resolves it once a write from the
// template replaced it. An unreadable unit leaves the notice as it is.
func UnitFallbackNoticeCheck(unitFile string) notices.Check {
	return func(ctx context.Context, n *notices.Service) error {
		restored, err := RestoredUnit(unitFile)
		if err != nil {
			return err
		}
		return n.Set(ctx, restored, notices.RaiseInput{
			Key:      UnitFallbackNoticeKey,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    i18n.Msg("notice.unit_fallback.title"),
			Body: i18n.Msg("notice.unit_fallback.body", "journalctl -u dockmesh",
				"sudo dockmesh system write-unit && sudo systemctl restart dockmesh"),
		})
	}
}

// ShellQuote returns s as one shell word: unchanged when it only holds
// characters no shell treats specially, else in single quotes.
func ShellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-+:@=,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
