package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/webtls"
)

// DefaultBaseURL is the public address used when neither DOCKMESH_BASE_URL
// nor the Base URL setting gives one.
const DefaultBaseURL = "http://localhost:8080"

type Config struct {
	HTTPAddr          string
	DataDir           string
	DBPath            string
	StacksRoot        string
	SecretsPath       string
	AuditGenesisPath  string
	SecretsKeyPath    string
	SecretsEncryptEnv bool
	ScannerBinary     string
	ScannerEnabled    bool
	ProxyEnabled      bool
	ProxyImage        string
	BaseURL           string
	AgentListen       string
	AgentPublicURL    string
	AgentSANs         string
	// AgentEnrollTokenTTL is how long a host enrollment token stays
	// valid (DOCKMESH_AGENT_ENROLL_TOKEN_TTL, Go duration). Zero keeps
	// the agents package default.
	AgentEnrollTokenTTL time.Duration
	// MetricsAuth gates the /metrics endpoint. Default true (require
	// metrics.read perm, so API-token scraping is needed). Set
	// DOCKMESH_METRICS_AUTH=false on trusted networks where the
	// scrape comes from a host-only firewalled Prometheus.
	MetricsAuth bool
	// P.12.3 observability: slog format (json|text), slog level (debug|
	// info|warn|error), and optional OTLP/gRPC trace exporter endpoint.
	// All default to sensible production settings (json logs + info
	// level + no tracing). Set DOCKMESH_OTEL_ENDPOINT to enable tracing.
	LogFormat    string
	LogLevel     string
	OTelEndpoint string
	OTelInsecure bool
	JWTSecret    []byte
	// SetupForce marks an install whose setup wizard has not finished
	// (DOCKMESH_SETUP_FORCE); see setup.Decide.
	SetupForce bool
	// TLSMode is what the web listener serves (DOCKMESH_TLS_MODE, see
	// webtls.Mode); empty is plain HTTP. TLSCertFile and TLSKeyFile are
	// DOCKMESH_TLS_CERT_FILE and DOCKMESH_TLS_KEY_FILE for mode file.
	TLSMode     string
	TLSCertFile string
	TLSKeyFile  string
	// UpdateRollbackKeep is how many rollback tags per image the
	// container updater keeps (DOCKMESH_UPDATE_ROLLBACK_KEEP).
	UpdateRollbackKeep int
}

func Load() (*Config, error) {
	paths := ResolvePaths(os.Getenv)
	cfg := &Config{
		DataDir:           paths.DataDir,
		DBPath:            paths.DBPath,
		StacksRoot:        paths.StacksRoot,
		SecretsPath:       paths.SecretsPath,
		AuditGenesisPath:  paths.AuditGenesisPath,
		SecretsKeyPath:    paths.SecretsKeyPath,
		SecretsEncryptEnv: envOr("DOCKMESH_SECRETS_ENCRYPT_ENV", "true") != "false",
		ScannerBinary:     envOr("DOCKMESH_SCANNER_BINARY", "grype"),
		ScannerEnabled:    envOr("DOCKMESH_SCANNER_ENABLED", "true") != "false",
		// Proxy is opt-in: many users already run Traefik or NPM.
		ProxyEnabled: envOr("DOCKMESH_PROXY_ENABLED", "false") == "true",
		ProxyImage:   envOr("DOCKMESH_PROXY_IMAGE", ""),
		BaseURL:      envOr("DOCKMESH_BASE_URL", DefaultBaseURL),
		// Remote-agent mTLS listener (concept §3.1). Empty = disabled.
		// AgentPublicURL is the wss:// URL printed in the install hint;
		// must be reachable by the agent host. AgentSANs adds extra
		// hostnames/IPs to the server cert (comma-separated).
		AgentListen:    envOr("DOCKMESH_AGENT_LISTEN", ":8443"),
		AgentPublicURL: envOr("DOCKMESH_AGENT_PUBLIC_URL", ""),
		AgentSANs:      envOr("DOCKMESH_AGENT_SANS", ""),
		MetricsAuth:    envOr("DOCKMESH_METRICS_AUTH", "true") != "false",
		LogFormat:      strings.ToLower(envOr("DOCKMESH_LOG_FORMAT", "json")),
		LogLevel:       strings.ToLower(envOr("DOCKMESH_LOG_LEVEL", "info")),
		OTelEndpoint:   envOr("DOCKMESH_OTEL_ENDPOINT", ""),
		OTelInsecure:   envOr("DOCKMESH_OTEL_INSECURE", "false") == "true",
		SetupForce:     envOr("DOCKMESH_SETUP_FORCE", "false") == "true",
		TLSMode:        os.Getenv("DOCKMESH_TLS_MODE"),
		TLSCertFile:    os.Getenv("DOCKMESH_TLS_CERT_FILE"),
		TLSKeyFile:     os.Getenv("DOCKMESH_TLS_KEY_FILE"),
	}
	mode, err := webtls.ParseMode(cfg.TLSMode)
	if err != nil {
		return nil, err
	}
	cfg.HTTPAddr = webtls.ListenAddr(mode, os.Getenv("DOCKMESH_HTTP_ADDR"))
	if mode == webtls.File && (!filepath.IsAbs(cfg.TLSCertFile) || !filepath.IsAbs(cfg.TLSKeyFile)) {
		return nil, errors.New("DOCKMESH_TLS_MODE=file needs absolute paths in DOCKMESH_TLS_CERT_FILE and DOCKMESH_TLS_KEY_FILE")
	}
	// Mirrors updater.DefaultRollbackKeep; config can't import updater.
	cfg.UpdateRollbackKeep = 3
	if v := os.Getenv("DOCKMESH_UPDATE_ROLLBACK_KEEP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("DOCKMESH_UPDATE_ROLLBACK_KEEP must be a positive number, got %q", v)
		}
		cfg.UpdateRollbackKeep = n
	}
	ttl, err := parseEnrollTokenTTL(os.Getenv("DOCKMESH_AGENT_ENROLL_TOKEN_TTL"))
	if err != nil {
		return nil, err
	}
	cfg.AgentEnrollTokenTTL = ttl
	secret, err := loadOrCreateJWTSecret(cfg.SecretsPath)
	if err != nil {
		return nil, fmt.Errorf("jwt secret: %w", err)
	}
	cfg.JWTSecret = secret
	return cfg, nil
}

func parseEnrollTokenTTL(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute || d > 30*24*time.Hour {
		return 0, fmt.Errorf("DOCKMESH_AGENT_ENROLL_TOKEN_TTL: want a duration between 1m and 720h, got %q", v)
	}
	return d, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const jwtSecretKey = "DOCKMESH_JWT_SECRET"

func loadOrCreateJWTSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, jwtSecretKey+"=") {
				hexStr := strings.TrimPrefix(line, jwtSecretKey+"=")
				out, decErr := hex.DecodeString(hexStr)
				if decErr != nil {
					return nil, decErr
				}
				if len(out) < 32 {
					return nil, errors.New("jwt secret too short")
				}
				return out, nil
			}
		}
		// File existed but no key — fall through to generate
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	content := jwtSecretKey + "=" + hex.EncodeToString(buf) + "\n"
	if err := writeNew(path, []byte(content)); err != nil {
		return nil, err
	}
	return buf, nil
}

// writeNew puts data at path through a fresh temp file and a rename, so a
// symlink planted at path is replaced rather than followed. A regular file
// at path passes its owner and mode on, so a root CLI rewriting the
// service's file does not lock the service out.
func writeNew(path string, data []byte) error {
	old, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := path + ".new-" + hex.EncodeToString(suffix)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- next to the configured secrets path
	if err != nil {
		return err
	}
	if old != nil && old.Mode().IsRegular() {
		err = keepOwner(f, old)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
