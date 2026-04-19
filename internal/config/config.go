package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	HTTPAddr          string
	DBPath            string
	StacksRoot        string
	SecretsPath       string
	AuditGenesisPath  string
	SecretsKeyPath    string
	SecretsEncryptEnv bool
	ScannerBinary     string
	ScannerEnabled    bool
	ProxyEnabled      bool
	BaseURL           string
	AgentListen       string
	AgentPublicURL    string
	AgentSANs         string
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
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:          envOr("DOCKMESH_HTTP_ADDR", ":8080"),
		DBPath:            envOr("DOCKMESH_DB_PATH", "./data/dockmesh.db"),
		StacksRoot:        envOr("DOCKMESH_STACKS_ROOT", "./stacks"),
		SecretsPath:       envOr("DOCKMESH_SECRETS_PATH", "./data/secrets.env"),
		AuditGenesisPath:  envOr("DOCKMESH_AUDIT_GENESIS_PATH", "./data/audit-genesis.sha256"),
		SecretsKeyPath:    envOr("DOCKMESH_SECRETS_KEY_PATH", "./data/secrets.age-key"),
		SecretsEncryptEnv: envOr("DOCKMESH_SECRETS_ENCRYPT_ENV", "true") != "false",
		ScannerBinary:     envOr("DOCKMESH_SCANNER_BINARY", "grype"),
		ScannerEnabled:    envOr("DOCKMESH_SCANNER_ENABLED", "true") != "false",
		// Proxy is opt-in: many users already run Traefik or NPM.
		ProxyEnabled: envOr("DOCKMESH_PROXY_ENABLED", "false") == "true",
		// BaseURL is used to build the OIDC redirect URL. Providers must
		// have <baseURL>/api/v1/auth/oidc/{slug}/callback whitelisted.
		BaseURL: envOr("DOCKMESH_BASE_URL", "http://localhost:8080"),
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
	}
	secret, err := loadOrCreateJWTSecret(cfg.SecretsPath)
	if err != nil {
		return nil, fmt.Errorf("jwt secret: %w", err)
	}
	cfg.JWTSecret = secret
	return cfg, nil
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
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return buf, nil
}
