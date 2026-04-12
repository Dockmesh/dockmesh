// Package scanner wraps a vulnerability scanner for container images.
// Phase 2 ships a subprocess implementation (internal/scanner/grype.go)
// that shells out to the `grype` binary; the concept §15.12 calls for an
// embedded Grype library later, which can replace the subprocess impl
// without touching callers as long as it satisfies the Scanner interface.
package scanner

import (
	"context"
	"time"
)

// Scanner scans a container image reference and returns a structured report.
type Scanner interface {
	// Scan runs a scan against the given image reference (e.g. "nginx:alpine"
	// or "sha256:..."). Implementations must respect ctx for cancellation.
	Scan(ctx context.Context, image string) (*Report, error)
	// Ready returns nil if the scanner is usable (binary installed, DB
	// downloaded, etc.). Called at startup and before scan attempts.
	Ready() error
	// Name identifies the scanner for audit + UI purposes ("grype", "trivy").
	Name() string
}

type Severity string

const (
	SevUnknown    Severity = "unknown"
	SevNegligible Severity = "negligible"
	SevLow        Severity = "low"
	SevMedium     Severity = "medium"
	SevHigh       Severity = "high"
	SevCritical   Severity = "critical"
)

// SeverityRank maps a severity to a number for sorting/filtering. Higher
// number = more serious.
func SeverityRank(s Severity) int {
	switch s {
	case SevCritical:
		return 5
	case SevHigh:
		return 4
	case SevMedium:
		return 3
	case SevLow:
		return 2
	case SevNegligible:
		return 1
	default:
		return 0
	}
}

type Vulnerability struct {
	ID       string   `json:"id"`       // e.g. CVE-2023-1234, GHSA-xxxx
	Severity Severity `json:"severity"`
	Package  string   `json:"package"`
	Version  string   `json:"version"`
	FixedIn  string   `json:"fixed_in,omitempty"`
	Type     string   `json:"type,omitempty"`        // deb / apk / npm / …
	URL      string   `json:"url,omitempty"`
}

// Summary counts vulnerabilities by severity. Keys are the lowercase
// severity string so they marshal naturally to JSON.
type Summary struct {
	Critical   int `json:"critical"`
	High       int `json:"high"`
	Medium     int `json:"medium"`
	Low        int `json:"low"`
	Negligible int `json:"negligible"`
	Unknown    int `json:"unknown"`
}

func (s *Summary) add(sev Severity) {
	switch sev {
	case SevCritical:
		s.Critical++
	case SevHigh:
		s.High++
	case SevMedium:
		s.Medium++
	case SevLow:
		s.Low++
	case SevNegligible:
		s.Negligible++
	default:
		s.Unknown++
	}
}

// Total returns the overall count across severities.
func (s *Summary) Total() int {
	return s.Critical + s.High + s.Medium + s.Low + s.Negligible + s.Unknown
}

type Report struct {
	Image           string          `json:"image"`
	Scanner         string          `json:"scanner"`
	ScannerVersion  string          `json:"scanner_version,omitempty"`
	ScannedAt       time.Time       `json:"scanned_at"`
	Summary         Summary         `json:"summary"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}
