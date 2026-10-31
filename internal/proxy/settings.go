package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
)

// Settings keys of the proxy-wide options. They are written only
// through Service.SaveSettings and the version switch.
const (
	KeyACMEEmail      = "proxy_acme_email"
	KeyACMECA         = "proxy_acme_ca"
	KeyTrustedProxies = "proxy_trusted_proxies"
	KeyCaddyVersion   = "proxy_caddy_version"
	KeyCaddyPrevious  = "proxy_caddy_previous"
)

// ManagedSettingKeys are the settings the generic settings API must not
// write, because they need validation or a container switch.
var ManagedSettingKeys = []string{KeyACMEEmail, KeyACMECA, KeyTrustedProxies, KeyCaddyVersion, KeyCaddyPrevious}

// Settings apply to every site. The zero value means Let's Encrypt
// without an account email and client addresses taken from the
// connection.
type Settings struct {
	ACMEEmail string `json:"acme_email"`
	// ACMECA is "" (Let's Encrypt), "letsencrypt_staging" or "zerossl".
	ACMECA string `json:"acme_ca"`
	// TrustedProxies is "" or "cloudflare".
	TrustedProxies string `json:"trusted_proxies"`
}

var ErrInvalidSettings = errors.New("invalid proxy settings")

var acmeDirectories = map[string]string{
	"":                    "",
	"letsencrypt_staging": "https://acme-staging-v02.api.letsencrypt.org/directory",
	"zerossl":             "https://acme.zerossl.com/v2/DV90",
}

// cloudflareRanges are Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips/).
var cloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

func (g Settings) validate() error {
	if g.ACMEEmail != "" {
		a, err := mail.ParseAddress(g.ACMEEmail)
		if err != nil || a.Address != g.ACMEEmail || hasPlaceholderOrControl(g.ACMEEmail) {
			return fmt.Errorf("%w: acme_email must be a plain address like ops@example.com", ErrInvalidSettings)
		}
	}
	if _, ok := acmeDirectories[g.ACMECA]; !ok {
		return fmt.Errorf("%w: acme_ca must be empty (Let's Encrypt), letsencrypt_staging or zerossl", ErrInvalidSettings)
	}
	if g.ACMECA == "zerossl" && g.ACMEEmail == "" {
		return fmt.Errorf("%w: ZeroSSL needs acme_email", ErrInvalidSettings)
	}
	switch g.TrustedProxies {
	case "", "cloudflare":
	default:
		return fmt.Errorf("%w: trusted_proxies must be empty or cloudflare", ErrInvalidSettings)
	}
	return nil
}

func (g Settings) acmeIssuer() caddyIssuer {
	return caddyIssuer{Module: "acme", CA: acmeDirectories[g.ACMECA], Email: g.ACMEEmail}
}

func (g Settings) trustedProxies() *caddyIPSource {
	if g.TrustedProxies != "cloudflare" {
		return nil
	}
	return &caddyIPSource{Source: "static", Ranges: cloudflareRanges}
}

// clientIPHeaders reads the client address from CF-Connecting-IP, which
// Cloudflare sets itself. X-Forwarded-For would start with whatever the
// visitor sent, letting them pass IP allow and deny lists.
func (g Settings) clientIPHeaders() []string {
	if g.TrustedProxies != "cloudflare" {
		return nil
	}
	return []string{"CF-Connecting-IP"}
}

// SetSettings gives the service the settings store for the proxy-wide
// options and the selected Caddy version. Call before Reconcile.
func (s *Service) SetSettings(st SettingsStore) {
	s.settings = st
	s.applyStoredVersion()
}

// ProxySettings returns the stored proxy-wide options.
func (s *Service) ProxySettings() Settings {
	if s.settings == nil {
		return Settings{}
	}
	return Settings{
		ACMEEmail:      s.settings.Get(KeyACMEEmail, ""),
		ACMECA:         s.settings.Get(KeyACMECA, ""),
		TrustedProxies: s.settings.Get(KeyTrustedProxies, ""),
	}
}

// SaveSettings validates, stores and applies the proxy-wide options.
func (s *Service) SaveSettings(ctx context.Context, g Settings) error {
	if err := g.validate(); err != nil {
		return err
	}
	if s.settings == nil {
		return errSettingsUnavailable
	}
	// Serialised with version switches, whose check compares Caddy's
	// config with the one built from these settings.
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	prev := s.ProxySettings()
	if err := s.storeSettings(ctx, g); err != nil {
		return err
	}
	if err := s.SyncFromDB(ctx); err != nil {
		// Caddy keeps its old config when it rejects a push; keep the
		// stored settings in line with it.
		if rbErr := s.storeSettings(ctx, prev); rbErr != nil {
			return fmt.Errorf("apply proxy settings: %w (restoring the old ones failed: %w)", err, rbErr)
		}
		return fmt.Errorf("apply proxy settings: %w", err)
	}
	return nil
}

func (s *Service) storeSettings(ctx context.Context, g Settings) error {
	for k, v := range map[string]string{KeyACMEEmail: g.ACMEEmail, KeyACMECA: g.ACMECA, KeyTrustedProxies: g.TrustedProxies} {
		if err := s.settings.Set(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}
