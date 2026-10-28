package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/pkg/version"
)

const maxEnrollResponse = 1 << 20

// enrollClient builds the HTTP client for the one-time enroll request.
// https requires a pinned CA: the CA file must hash to the configured
// fingerprint and the server's chain must verify against it. Plain http
// is only allowed with --insecure-enroll and is never the default.
func enrollClient(cfg agentConfig) (*http.Client, error) {
	u, err := url.Parse(cfg.enrollURL)
	if err != nil {
		return nil, fmt.Errorf("enroll url: %w", err)
	}
	switch u.Scheme {
	case "https":
		if cfg.caFingerprint == "" {
			return nil, errors.New("DOCKMESH_CA_FINGERPRINT is required to enroll over https — " +
				"re-run the install command from the Hosts page")
		}
		pool, err := loadPinnedCA(cfg.enrollCAFile, cfg.caFingerprint)
		if err != nil {
			return nil, err
		}
		return &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		}, nil
	case "http":
		if !cfg.insecureEnroll {
			return nil, errors.New("refusing to enroll over plain http (token and client key would travel " +
				"unencrypted) — use the https enroll URL from the install command, or pass " +
				"--insecure-enroll for a lab setup")
		}
		slog.Warn("INSECURE ENROLLMENT: sending the enrollment token and receiving the client key over plain http; " +
			"anyone on the network path can take over this agent. Use only in an isolated lab.")
		return &http.Client{Timeout: 30 * time.Second}, nil
	default:
		return nil, fmt.Errorf("enroll url: unsupported scheme %q", u.Scheme)
	}
}

// loadPinnedCA reads the CA certificate from path and returns a pool
// containing only that cert, provided its SHA-256 matches fingerprint.
func loadPinnedCA(path, fingerprint string) (*x509.CertPool, error) {
	if path == "" {
		return nil, errors.New("DOCKMESH_ENROLL_CA_FILE is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read enroll CA: %w", err)
	}
	cert, err := parseSingleCertPEM(raw)
	if err != nil {
		return nil, fmt.Errorf("enroll CA %s: %w", path, err)
	}
	if err := checkCAFingerprint(cert, fingerprint); err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool, nil
}

func parseSingleCertPEM(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM certificate found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func checkCAFingerprint(cert *x509.Certificate, fingerprint string) error {
	if !cert.IsCA {
		return errors.New("pinned certificate is not a CA")
	}
	sum := sha256.Sum256(cert.Raw)
	got := hex.EncodeToString(sum[:])
	want := normalizeFingerprint(fingerprint)
	if got != want {
		return fmt.Errorf("CA fingerprint mismatch: pinned %s, got %s", want, got)
	}
	return nil
}

// normalizeFingerprint accepts the hex form with or without colons and in
// either case, as printed by openssl or copied from the UI.
func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fp), ":", ""))
}

func enroll(cfg agentConfig, certPath, keyPath, caPath, urlPath string) error {
	httpClient, err := enrollClient(cfg)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	body := agents.EnrollRequest{
		Token:         cfg.token,
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Version:       version.Version,
		DockerVersion: dockerDaemonVersion(context.Background()),
	}
	buf, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, cfg.enrollURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("enroll http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var er agents.EnrollResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEnrollResponse)).Decode(&er); err != nil {
		return err
	}
	if er.ClientCert == "" || er.ClientKey == "" || er.CACert == "" {
		return errors.New("server returned empty cert payload")
	}
	// The returned CA becomes the trust anchor for every later connection,
	// so it has to be the pinned one even when the transport was http.
	if cfg.caFingerprint != "" {
		caCert, err := parseSingleCertPEM([]byte(er.CACert))
		if err != nil {
			return fmt.Errorf("server CA: %w", err)
		}
		if err := checkCAFingerprint(caCert, cfg.caFingerprint); err != nil {
			return fmt.Errorf("server CA: %w", err)
		}
	}
	if err := os.WriteFile(certPath, []byte(er.ClientCert), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(er.ClientKey), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(caPath, []byte(er.CACert), 0o644); err != nil {
		return err
	}
	if er.AgentURL != "" {
		_ = os.WriteFile(urlPath, []byte(er.AgentURL), 0o644)
	}
	slog.Info("cert persisted", "agent_id", er.AgentID, "cn", er.AgentName)
	return nil
}
