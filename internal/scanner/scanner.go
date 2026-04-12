package scanner

import "context"

// Scanner is the vulnerability scanner interface. Phase 2 wires this to Grype.
type Scanner interface {
	Scan(ctx context.Context, image string) (*Report, error)
}

type Severity string

const (
	SevCritical Severity = "critical"
	SevHigh     Severity = "high"
	SevMedium   Severity = "medium"
	SevLow      Severity = "low"
	SevNegl     Severity = "negligible"
)

type Vulnerability struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Package  string   `json:"package"`
	Version  string   `json:"version"`
	FixedIn  string   `json:"fixed_in,omitempty"`
}

type Report struct {
	Image           string          `json:"image"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

// TODO(phase2): implement via embedded Grype binary or library.
type Stub struct{}

func (Stub) Scan(ctx context.Context, image string) (*Report, error) {
	return &Report{Image: image}, nil
}
