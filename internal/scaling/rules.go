// Package scaling implements single-host auto-scaling for compose
// services (P.8). Rules live in each stack's .dockmesh.meta.json and
// are evaluated against metrics sampled by internal/metrics.
package scaling

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// ScalingConfig is the top-level scaling section inside
// .dockmesh.meta.json.
type ScalingConfig struct {
	Enabled bool   `json:"enabled"`
	Rules   []Rule `json:"rules"`
}

// Rule is one auto-scaling rule for a specific service.
type Rule struct {
	Service         string          `json:"service"`
	MinReplicas     int             `json:"min_replicas"`
	MaxReplicas     int             `json:"max_replicas"`
	ScaleUp         ThresholdConfig `json:"scale_up"`
	ScaleDown       ThresholdConfig `json:"scale_down"`
	CooldownSeconds int             `json:"cooldown_seconds"`
	// CPUUnit is the unit of CPU thresholds: "host" (share of the whole
	// host) or "core" (100 per busy core). Rules saved without one predate
	// the choice and use "core".
	CPUUnit string `json:"cpu_unit,omitempty"`
}

// CPU threshold units.
const (
	CPUUnitHost = "host"
	CPUUnitCore = "core"
)

// Unit returns the rule's CPU unit, "core" for rules saved without one.
func (r Rule) Unit() string {
	if r.CPUUnit == CPUUnitHost {
		return CPUUnitHost
	}
	return CPUUnitCore
}

// ThresholdConfig describes when to trigger a scale action.
type ThresholdConfig struct {
	Metric           string  `json:"metric"`            // "cpu" | "memory"
	ThresholdPercent float64 `json:"threshold_percent"` // 0-100
	DurationSeconds  int     `json:"duration_seconds"`  // how long the threshold must be exceeded
}

// metric is the threshold's metric in lower case, else fallback, else "cpu".
func (t ThresholdConfig) metric(fallback string) string {
	if m := strings.ToLower(t.Metric); m != "" {
		return m
	}
	if fallback != "" {
		return fallback
	}
	return "cpu"
}

// Validate checks a ScalingConfig for structural errors.
func (c *ScalingConfig) Validate() error {
	for i, r := range c.Rules {
		if r.Service == "" {
			return fmt.Errorf("rule[%d]: service is required", i)
		}
		if r.MinReplicas < 0 {
			return fmt.Errorf("rule[%d]: min_replicas must be >= 0", i)
		}
		if r.MaxReplicas < 1 {
			return fmt.Errorf("rule[%d]: max_replicas must be >= 1", i)
		}
		if r.MinReplicas > r.MaxReplicas {
			return fmt.Errorf("rule[%d]: min_replicas > max_replicas", i)
		}
		if err := validateThreshold("scale_up", r.ScaleUp); err != nil {
			return fmt.Errorf("rule[%d]: %w", i, err)
		}
		if err := validateThreshold("scale_down", r.ScaleDown); err != nil {
			return fmt.Errorf("rule[%d]: %w", i, err)
		}
		if r.CooldownSeconds < 0 {
			return fmt.Errorf("rule[%d]: cooldown_seconds must be >= 0", i)
		}
		switch r.CPUUnit {
		case "", CPUUnitHost, CPUUnitCore:
		default:
			return fmt.Errorf("rule[%d]: cpu_unit must be %q or %q", i, CPUUnitHost, CPUUnitCore)
		}
	}
	return nil
}

func validateThreshold(name string, t ThresholdConfig) error {
	switch strings.ToLower(t.Metric) {
	case "cpu", "memory", "":
		// ok
	default:
		return fmt.Errorf("%s: unsupported metric %q (use cpu or memory)", name, t.Metric)
	}
	if t.ThresholdPercent < 0 || t.ThresholdPercent > 100 {
		return fmt.Errorf("%s: threshold_percent must be 0-100", name)
	}
	if t.DurationSeconds < 0 {
		return fmt.Errorf("%s: duration_seconds must be >= 0", name)
	}
	return nil
}

// MetaFile represents the full .dockmesh.meta.json content. Other
// slices (migration hooks, etc.) add their own fields here.
type MetaFile struct {
	Scaling           *ScalingConfig  `json:"scaling,omitempty"`
	Migration         json.RawMessage `json:"migration,omitempty"`          // preserved for future P.9
	ActiveEnvironment string          `json:"active_environment,omitempty"` // P.12.8 — name of the compose.<name>.yaml overlay to apply by default
}

// LoadMeta reads and parses the .dockmesh.meta.json from a stack dir.
// Returns nil (no error) if the file doesn't exist.
func LoadMeta(stackDir string) (*MetaFile, error) {
	path := filepath.Join(stackDir, stacks.MetaFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m MetaFile
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &m, nil
}

// SaveMeta writes the .dockmesh.meta.json back to disk.
func SaveMeta(stackDir string, m *MetaFile) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Through a root, so a symlink a container put in place of the file
	// can't send the write outside the stack directory.
	root, err := os.OpenRoot(stackDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(stacks.MetaFileName, data, 0o644)
}

// LoadRules loads the scaling config for a specific stack directory.
// Returns nil (no error) if no scaling section exists.
func LoadRules(stackDir string) (*ScalingConfig, error) {
	m, err := LoadMeta(stackDir)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Scaling == nil {
		return nil, nil
	}
	return m.Scaling, nil
}
