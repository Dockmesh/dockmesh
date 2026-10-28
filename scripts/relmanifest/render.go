package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const advisoryURL = "https://github.com/Dockmesh/dockmesh/security/advisories/"

type Manifest struct {
	Schema         int        `json:"schema"`
	Version        string     `json:"version"`
	Date           string     `json:"date"`
	Severity       string     `json:"severity"`
	Summary        string     `json:"summary"`
	Changes        Changes    `json:"changes"`
	Advisories     []Advisory `json:"advisories"`
	Actions        []Action   `json:"actions"`
	MinUpgradeFrom string     `json:"min_upgrade_from"`
}

type Changes struct {
	Added    []string `json:"added"`
	Changed  []string `json:"changed"`
	Fixed    []string `json:"fixed"`
	Security []string `json:"security"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func BuildManifest(s *Section) Manifest {
	return Manifest{
		Schema:   1,
		Version:  s.Version,
		Date:     s.Date,
		Severity: s.Severity,
		Summary:  s.Summary,
		Changes: Changes{
			Added:    nonNil(s.Added),
			Changed:  nonNil(s.Changed),
			Fixed:    nonNil(s.Fixed),
			Security: nonNil(s.Security),
		},
		Advisories:     nonNil(s.Advisories),
		Actions:        nonNil(s.Actions),
		MinUpgradeFrom: s.MinUpgradeFrom,
	}
}

func ManifestJSON(s *Section) ([]byte, error) {
	b, err := json.MarshalIndent(BuildManifest(s), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RenderNotes produces the GitHub release body. Security and upgrade actions
// come first because they are what a reader has to act on.
func RenderNotes(s *Section) string {
	var b strings.Builder
	switch s.Severity {
	case "security":
		b.WriteString("**Security release.** Update as soon as possible.\n\n")
	case "recommended":
		b.WriteString("**Recommended update.**\n\n")
	}
	if s.Summary != "" {
		b.WriteString(s.Summary + "\n\n")
	}

	if len(s.Advisories)+len(s.Security) > 0 {
		b.WriteString("### Security\n\n")
		for _, a := range s.Advisories {
			fmt.Fprintf(&b, "- [%s](%s%s) (%s): %s\n", a.ID, advisoryURL, a.ID, a.Severity, a.Title)
		}
		writeBullets(&b, s.Security)
		b.WriteString("\n")
	}
	if len(s.Actions) > 0 {
		b.WriteString("### Upgrade notes\n\n")
		for _, a := range s.Actions {
			who := "Everyone"
			if a.Audience == "admins" {
				who = "Admins"
			}
			fmt.Fprintf(&b, "- **%s:** %s\n", who, a.Text)
		}
		b.WriteString("\n")
	}
	for _, sec := range []struct {
		title string
		items []string
	}{{"Added", s.Added}, {"Changed", s.Changed}, {"Fixed", s.Fixed}} {
		if len(sec.items) == 0 {
			continue
		}
		b.WriteString("### " + sec.title + "\n\n")
		writeBullets(&b, sec.items)
		b.WriteString("\n")
	}
	if s.MinUpgradeFrom != "" {
		fmt.Fprintf(&b, "Direct upgrades are supported from %s or later.\n", s.MinUpgradeFrom)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeBullets(b *strings.Builder, items []string) {
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
}
