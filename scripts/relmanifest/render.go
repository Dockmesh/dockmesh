package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/changelog"
)

const advisoryURL = "https://github.com/Dockmesh/dockmesh/security/advisories/"

func ManifestJSON(s *changelog.Section) ([]byte, error) {
	b, err := json.MarshalIndent(changelog.BuildManifest(s), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RenderNotes produces the GitHub release body. Security and upgrade actions
// come first because they are what a reader has to act on.
func RenderNotes(s *changelog.Section) string {
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
	if len(s.Highlights) > 0 {
		b.WriteString("### Highlights\n\n")
		writeBullets(&b, s.Highlights)
		b.WriteString("\n")
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

const repoURL = "https://github.com/Dockmesh/dockmesh"

// RenderBody produces the short GitHub release body: what to run, what to
// check afterwards and where the advisories and the full changelog are.
func RenderBody(s *changelog.Section) string {
	var b strings.Builder
	switch s.Severity {
	case "security":
		if n := len(s.Advisories); n > 0 {
			fmt.Fprintf(&b, "This is a security release. It fixes %d %s; update as soon as possible.\n\n", n, plural(n, "vulnerability", "vulnerabilities"))
		} else {
			b.WriteString("This is a security release; update as soon as possible.\n\n")
		}
	case "recommended":
		b.WriteString("This is a recommended update.\n\n")
	}
	if s.Summary != "" {
		b.WriteString(s.Summary + "\n\n")
	}

	b.WriteString("## How to upgrade\n\n```\ncurl -fsSL https://get.dockmesh.dev | sudo bash && sudo systemctl restart dockmesh\n```\n\n")
	fmt.Fprintf(&b, "If you run Dockmesh in a container you built yourself, rebuild it from the v%s tag and recreate the container. Then check that the version shown in the UI or printed by `dockmesh version` is %s; if it is not, the old version is still running. Finally update the agents from the Hosts page.\n\n", s.Version, s.Version)

	if len(s.Actions) > 0 {
		b.WriteString("## After upgrading, check\n\n")
		for _, a := range s.Actions {
			b.WriteString("- " + a.Text + "\n")
		}
		b.WriteString("\n")
	}
	if len(s.Advisories) > 0 {
		b.WriteString("## Security\n\n")
		for _, a := range s.Advisories {
			fmt.Fprintf(&b, "- [%s](%s%s) (%s): %s\n", a.ID, advisoryURL, a.ID, a.Severity, a.Title)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Details\n\nAll changes and the full upgrade notes: [CHANGELOG.md, %s](%s/blob/v%s/CHANGELOG.md#%s)\n", s.Version, repoURL, s.Version, headingAnchor(s))
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// headingAnchor mirrors GitHub's slug of "## [v] - date [severity]".
func headingAnchor(s *changelog.Section) string {
	h := strings.ToLower(fmt.Sprintf("%s - %s %s", s.Version, s.Date, s.Severity))
	var b strings.Builder
	for _, r := range h {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeBullets(b *strings.Builder, items []string) {
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
}
