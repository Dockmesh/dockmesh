package main

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// defaultDataDir matches install-agent.sh.
	defaultDataDir = "/var/lib/dockmesh-agent"
	// legacyDataDir is the default of older agent binaries; an agent
	// enrolled only there keeps using it.
	legacyDataDir = "/var/lib/dockmesh"
	// agentEnvFile is the config install-agent.sh writes.
	agentEnvFile = "/etc/dockmesh-agent/agent.env"
)

func dataDir() string {
	return resolveDataDir(os.Getenv("DOCKMESH_DATA_DIR"), fileExists)
}

func resolveDataDir(env string, exists func(string) bool) string {
	if env != "" {
		return env
	}
	if !exists(filepath.Join(defaultDataDir, "agent.crt")) && exists(filepath.Join(legacyDataDir, "agent.crt")) {
		return legacyDataDir
	}
	return defaultDataDir
}

// loadAgentEnvFile sets the variables of agentEnvFile that are not set
// yet, so `dockmesh-agent status` run by hand sees the service's config.
// Under systemd EnvironmentFile= has set them already. A file root does
// not own, or that others may write, is ignored.
func loadAgentEnvFile(path string) {
	if data, ok := readRootOwnedFile(path); ok {
		applyEnv(data)
	}
}

func applyEnv(data []byte) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !strings.HasPrefix(k, "DOCKMESH_") {
			continue
		}
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
}
