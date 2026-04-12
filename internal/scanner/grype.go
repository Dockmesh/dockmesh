package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GrypeCLI runs the grype binary as a subprocess. Pragmatic choice for
// Phase 2 — the concept §15.12 wants an embedded library, but grype's
// library API has a huge dep graph. Swapping in an embedded impl later
// is a matter of writing a new Scanner and pointing main.go at it.
type GrypeCLI struct {
	Binary  string        // defaults to "grype"
	Timeout time.Duration // per-scan timeout, default 5 min
}

func NewGrypeCLI(binary string) *GrypeCLI {
	if binary == "" {
		binary = "grype"
	}
	return &GrypeCLI{Binary: binary, Timeout: 5 * time.Minute}
}

func (g *GrypeCLI) Name() string { return "grype" }

func (g *GrypeCLI) Ready() error {
	if _, err := exec.LookPath(g.Binary); err != nil {
		return fmt.Errorf("grype binary not found in PATH: %w", err)
	}
	return nil
}

func (g *GrypeCLI) Scan(ctx context.Context, image string) (*Report, error) {
	if image == "" {
		return nil, errors.New("image required")
	}
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}

	// Use the docker daemon as source so we don't re-pull via registry.
	// Grype accepts `docker:<ref>` to force that source.
	ref := image
	if !strings.HasPrefix(ref, "docker:") && !strings.HasPrefix(ref, "registry:") {
		ref = "docker:" + ref
	}

	cmd := exec.CommandContext(ctx, g.Binary, ref, "-o", "json", "--quiet")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("grype exit %d: %s", ee.ExitCode(), string(ee.Stderr))
		}
		return nil, fmt.Errorf("run grype: %w", err)
	}

	var raw grypeOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse grype json: %w", err)
	}

	rep := &Report{
		Image:          image,
		Scanner:        "grype",
		ScannerVersion: raw.Descriptor.Version,
		ScannedAt:      time.Now().UTC(),
	}
	for _, m := range raw.Matches {
		sev := normalizeSeverity(m.Vulnerability.Severity)
		v := Vulnerability{
			ID:       m.Vulnerability.ID,
			Severity: sev,
			Package:  m.Artifact.Name,
			Version:  m.Artifact.Version,
			Type:     m.Artifact.Type,
			URL:      m.Vulnerability.DataSource,
		}
		if len(m.Vulnerability.Fix.Versions) > 0 {
			v.FixedIn = strings.Join(m.Vulnerability.Fix.Versions, ", ")
		}
		rep.Vulnerabilities = append(rep.Vulnerabilities, v)
		rep.Summary.add(sev)
	}
	return rep, nil
}

// grypeOutput is the subset of `grype -o json` we care about. Grype's full
// schema has many more fields we don't use.
type grypeOutput struct {
	Matches []grypeMatch `json:"matches"`
	Descriptor struct {
		Version string `json:"version"`
	} `json:"descriptor"`
}

type grypeMatch struct {
	Vulnerability grypeVuln     `json:"vulnerability"`
	Artifact      grypeArtifact `json:"artifact"`
}

type grypeVuln struct {
	ID         string   `json:"id"`
	Severity   string   `json:"severity"`
	DataSource string   `json:"dataSource"`
	Fix        grypeFix `json:"fix"`
}

type grypeFix struct {
	Versions []string `json:"versions"`
	State    string   `json:"state"`
}

type grypeArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
}

func normalizeSeverity(s string) Severity {
	switch strings.ToLower(s) {
	case "critical":
		return SevCritical
	case "high":
		return SevHigh
	case "medium":
		return SevMedium
	case "low":
		return SevLow
	case "negligible":
		return SevNegligible
	default:
		return SevUnknown
	}
}
