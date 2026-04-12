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
	HTTPAddr    string
	DBPath      string
	StacksRoot  string
	SecretsPath string
	JWTSecret   []byte
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:    envOr("DOCKMESH_HTTP_ADDR", ":8080"),
		DBPath:      envOr("DOCKMESH_DB_PATH", "./data/dockmesh.db"),
		StacksRoot:  envOr("DOCKMESH_STACKS_ROOT", "./stacks"),
		SecretsPath: envOr("DOCKMESH_SECRETS_PATH", "./data/secrets.env"),
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
