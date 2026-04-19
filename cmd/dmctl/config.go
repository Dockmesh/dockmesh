package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is persisted at ~/.config/dmctl/config.json (or the platform
// equivalent). Kept tiny deliberately — dmctl is stateless between
// invocations aside from server+token.
//
// Format is JSON (not YAML) because it's what stdlib gives us for free
// and the field count never needs more than 4-5 keys.
type Config struct {
	Server   string `json:"server,omitempty"`
	Token    string `json:"token,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// configPath returns the OS-appropriate config file location. Uses
// os.UserConfigDir so Windows lands at %APPDATA%\dmctl\config.json
// and Linux/macOS at ~/.config/dmctl/config.json.
func configPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dmctl", "config.json"), nil
}

func loadConfig() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

func saveConfig(c *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600 — contains a bearer token; nobody else on the host should read it.
	return os.WriteFile(path, data, 0o600)
}

// resolveCredentials figures out which server URL + token to use for
// this invocation, walking flag → env → config in priority order.
// Returns an error when nothing resolves a server or token, so
// subcommands get a clean "log in first" message instead of a
// confusing DNS failure.
func resolveCredentials() (server, token string, insecure bool, err error) {
	cfg, cErr := loadConfig()
	if cErr != nil {
		// Config read failure is non-fatal if flags / env supply both.
		cfg = &Config{}
	}

	server = firstNonEmpty(flagServer, os.Getenv("DMCTL_SERVER"), cfg.Server)
	token = firstNonEmpty(flagToken, os.Getenv("DMCTL_TOKEN"), cfg.Token)
	// --insecure on the CLI always wins; otherwise inherit config.
	insecure = flagInsecure || cfg.Insecure

	if server == "" {
		return "", "", false, fmt.Errorf("no server configured — run `dmctl login <server>` or set DMCTL_SERVER")
	}
	if token == "" {
		return "", "", false, fmt.Errorf("no token configured — run `dmctl login <server>` or set DMCTL_TOKEN")
	}
	return server, token, insecure, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
