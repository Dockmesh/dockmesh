package main

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"os"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/webtls"
)

// listenerTLS returns what the web listener of a set-up server serves in
// cfg.TLSMode: nil for plain HTTP. Behind a proxy it also trusts the
// bind address as that proxy when the operator named none.
func listenerTLS(cfg *config.Config, m *pki.Manager) (*tls.Config, *x509.Certificate, error) {
	mode := webtls.Mode(cfg.TLSMode)
	switch mode {
	case webtls.Internal:
		cert, err := m.EnsureUICert(setupCertHosts(cfg.BaseURL))
		if err != nil {
			return nil, nil, err
		}
		slog.Info("web listener serves the internal certificate", "sha256", pki.Fingerprint(cert.Leaf.Raw))
		return webtls.ServerConfig(cert, nil), cert.Leaf, nil
	case webtls.File:
		r, err := webtls.NewReloader(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, nil, err
		}
		leaf := r.Certificate().Leaf
		slog.Info("web listener serves the configured certificate, reloaded when the files change", "cert", cfg.TLSCertFile, "not_after", leaf.NotAfter)
		return webtls.ServerConfig(nil, r), leaf, nil
	}
	if mode.BehindProxy() && os.Getenv("DOCKMESH_TRUSTED_PROXIES") == "" {
		if def := webtls.DefaultTrustedProxies(mode, cfg.HTTPAddr); def != "" {
			if err := os.Setenv("DOCKMESH_TRUSTED_PROXIES", def); err != nil {
				return nil, nil, err
			}
			slog.Info("trusting the reverse proxy on the bind address", "trusted_proxies", def)
		}
	}
	return nil, nil, nil
}
