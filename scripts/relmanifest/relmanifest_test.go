package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/changelog"
)

const sample = `# Changelog

Preamble with ## not at line start and - bullets that are ignored.

## [Unreleased]

### Fixed
- Pending fix.

## [0.3.4] - 2026-11-05 [security]

Closes several access-control issues.
Everyone has to sign in again.

Minimum upgrade from: 0.3.0

### Highlights
- Sessions are typed per purpose.

### Security
- GHSA-2xq8-c4fv-9m7w (critical): MFA could be disabled with a half-finished login.
- Session tokens are now typed
  per purpose.

### Upgrade notes
- [re-login, all] Sign in again after the upgrade.
- [sftp-host-key, admins] Re-save each SFTP backup target.

### Added
- Release manifest.

### Changed

### Fixed
- One.
- Two.

## [0.3.3] - 2026-10-01 [normal]

### Fixed
- Older fix.

[0.3.4]: https://github.com/Dockmesh/dockmesh/releases/tag/v0.3.4
`

func TestParseSample(t *testing.T) {
	sections, err := changelog.Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3", len(sections))
	}
	s := changelog.Find(sections, "v0.3.4")
	if s == nil {
		t.Fatal("0.3.4 not found via tag name")
	}
	want := changelog.Manifest{
		Schema:     1,
		Version:    "0.3.4",
		Date:       "2026-11-05",
		Severity:   "security",
		Summary:    "Closes several access-control issues.\nEveryone has to sign in again.",
		Highlights: []string{"Sessions are typed per purpose."},
		Changes: changelog.Changes{
			Added:    []string{"Release manifest."},
			Changed:  []string{},
			Fixed:    []string{"One.", "Two."},
			Security: []string{"Session tokens are now typed per purpose."},
		},
		Advisories: []changelog.Advisory{{ID: "GHSA-2xq8-c4fv-9m7w", Severity: "critical", Title: "MFA could be disabled with a half-finished login."}},
		Actions: []changelog.Action{
			{ID: "re-login", Audience: "all", Text: "Sign in again after the upgrade."},
			{ID: "sftp-host-key", Audience: "admins", Text: "Re-save each SFTP backup target."},
		},
		MinUpgradeFrom: "0.3.0",
	}
	if got := changelog.BuildManifest(s); !reflect.DeepEqual(got, want) {
		t.Errorf("manifest mismatch\n got %+v\nwant %+v", got, want)
	}
	if old := changelog.Find(sections, "0.3.3"); old == nil || len(old.Fixed) != 1 || old.Fixed[0] != "Older fix." {
		t.Errorf("0.3.3 parsed wrong (link reference must not leak into Fixed): %+v", old)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"missing severity", "## [1.0.0] - 2026-01-01\n", "malformed version header"},
		{"bad severity", "## [1.0.0] - 2026-01-01 [urgent]\n", "malformed version header"},
		{"bad date", "## [1.0.0] - 2026-1-1 [normal]\n", "malformed version header"},
		{"unknown subsection", "## [Unreleased]\n### Removed\n- x\n", "unknown subsection"},
		{"prose under subsection", "## [Unreleased]\n### Added\nsome prose\n", "expected a \"- \" bullet"},
		{"untagged upgrade note", "## [Unreleased]\n### Upgrade notes\n- Just do it.\n", "must start with"},
		{"bad audience", "## [Unreleased]\n### Upgrade notes\n- [x, users] Do it.\n", "must start with"},
		{"duplicate action", "## [Unreleased]\n### Upgrade notes\n- [x, all] A.\n- [x, admins] B.\n", "duplicate upgrade action"},
		{"duplicate section", "## [Unreleased]\n## [Unreleased]\n", "duplicate section"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := changelog.Parse(tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAdvisoryLookalikeStaysPlainSecurityEntry(t *testing.T) {
	sections, err := changelog.Parse("## [Unreleased]\n### Security\n- GHSA-2xq8-c4fv-9m7w (urgent): not a valid severity.\n")
	if err != nil {
		t.Fatal(err)
	}
	s := sections[0]
	if len(s.Advisories) != 0 || len(s.Security) != 1 {
		t.Errorf("advisories=%v security=%v", s.Advisories, s.Security)
	}
}

func TestRenderNotes(t *testing.T) {
	sections, err := changelog.Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	got := RenderNotes(changelog.Find(sections, "0.3.4"))
	want := `**Security release.** Update as soon as possible.

Closes several access-control issues.
Everyone has to sign in again.

### Highlights

- Sessions are typed per purpose.

### Security

- [GHSA-2xq8-c4fv-9m7w](https://github.com/Dockmesh/dockmesh/security/advisories/GHSA-2xq8-c4fv-9m7w) (critical): MFA could be disabled with a half-finished login.
- Session tokens are now typed per purpose.

### Upgrade notes

- **Everyone:** Sign in again after the upgrade.
- **Admins:** Re-save each SFTP backup target.

### Added

- Release manifest.

### Fixed

- One.
- Two.

Direct upgrades are supported from 0.3.0 or later.
`
	if got != want {
		t.Errorf("notes mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestRenderBody(t *testing.T) {
	src := `## [0.4.0] - 2026-10-29 [security]

Fixes two issues.

### Security
- GHSA-2xq8-c4fv-9m7w (critical): MFA could be disabled.
- GHSA-3xq8-c4fv-9m7w (high): Another one.

### Upgrade notes
- [re-login, all] Sign in again once.
- [sftp, admins] Re-save each SFTP target.

### Fixed
- Long list entry that stays out of the body.
`
	sections, err := changelog.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	got := RenderBody(sections[0])
	for _, want := range []string{
		"This is a security release. It fixes 2 vulnerabilities;",
		"Fixes two issues.",
		"sudo systemctl restart dockmesh",
		"rebuild it from the v0.4.0 tag",
		"`dockmesh version` is 0.4.0",
		"- Sign in again once.\n- Re-save each SFTP target.\n",
		"- [GHSA-2xq8-c4fv-9m7w](https://github.com/Dockmesh/dockmesh/security/advisories/GHSA-2xq8-c4fv-9m7w) (critical): MFA could be disabled.",
		"blob/v0.4.0/CHANGELOG.md#040---2026-10-29-security)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body lacks %q\n%s", want, got)
		}
	}
	for _, bad := range []string{"Long list entry", "### "} {
		if strings.Contains(got, bad) {
			t.Errorf("body contains %q", bad)
		}
	}
}

func TestRenderBodyNormalNoAdvisories(t *testing.T) {
	sections, err := changelog.Parse("## [1.0.0] - 2026-01-01 [normal]\n\n### Fixed\n- A fix.\n")
	if err != nil {
		t.Fatal(err)
	}
	got := RenderBody(sections[0])
	for _, bad := range []string{"security release", "After upgrading", "## Security"} {
		if strings.Contains(got, bad) {
			t.Errorf("body contains %q\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "#100---2026-01-01-normal)") {
		t.Errorf("anchor wrong\n%s", got)
	}
}

func TestReleaseCommandWritesBody(t *testing.T) {
	dir := t.TempDir()
	cl := writeFile(t, dir, "CHANGELOG.md", sample)
	body := filepath.Join(dir, "body.md")
	args := []string{"release", "-changelog", cl, "-version", "v0.3.4", "-notes", filepath.Join(dir, "n"), "-body", body, "-manifest", filepath.Join(dir, "m")}
	if err := run(args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## How to upgrade") {
		t.Errorf("body = %s", b)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReleaseCommand(t *testing.T) {
	dir := t.TempDir()
	cl := writeFile(t, dir, "CHANGELOG.md", sample)
	notes := filepath.Join(dir, "notes.md")
	manifest := filepath.Join(dir, "release-manifest.json")

	tests := []struct {
		name    string
		version string
		wantErr string
	}{
		{"tag with v", "v0.3.4", ""},
		{"missing section", "v0.3.5", "has no section for v0.3.5"},
		{"unreleased is not releasable", "Unreleased", "has no section"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run([]string{"release", "-changelog", cl, "-version", tt.version, "-notes", notes, "-manifest", manifest}, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"schema", "version", "date", "severity", "summary", "changes", "advisories", "actions", "min_upgrade_from"} {
				if _, ok := m[k]; !ok {
					t.Errorf("manifest lacks %q", k)
				}
			}
		})
	}
}

func TestReleaseRejectsEmptySection(t *testing.T) {
	dir := t.TempDir()
	cl := writeFile(t, dir, "CHANGELOG.md", "## [1.0.0] - 2026-01-01 [normal]\n\nNothing.\n")
	err := run([]string{"release", "-changelog", cl, "-version", "1.0.0", "-notes", filepath.Join(dir, "n"), "-manifest", filepath.Join(dir, "m")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no entries") {
		t.Errorf("err = %v", err)
	}
}

// cut returns a changelog where the base's Unreleased entry moved into a
// new version section with n highlights.
func cut(version string, n int) string {
	var hl string
	if n > 0 {
		hl = "### Highlights\n" + strings.Repeat("- A highlight.\n", n) + "\n"
	}
	return "# Changelog\n\n## [Unreleased]\n\n## [" + version + "] - 2026-10-20 [normal]\n\n" + hl +
		"### Fixed\n- Old entry.\n\n## [0.3.3] - 2026-10-01 [normal]\n\n### Fixed\n- x\n"
}

func TestReleaseRequiresHighlightsForMinor(t *testing.T) {
	tests := []struct {
		version string
		n       int
		wantErr string
	}{
		{"0.4.0", 3, ""},
		{"0.4.0", 0, "has 0"},
		{"0.4.0-rc.1", 1, "has 1"},
		{"0.3.4", 0, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%d", tt.version, tt.n), func(t *testing.T) {
			dir := t.TempDir()
			cl := writeFile(t, dir, "CHANGELOG.md", cut(tt.version, tt.n))
			err := run([]string{"release", "-changelog", cl, "-version", tt.version, "-notes", filepath.Join(dir, "n"), "-manifest", filepath.Join(dir, "m")}, &bytes.Buffer{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckCommand(t *testing.T) {
	base := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n- Old entry.\n\n## [0.3.3] - 2026-10-01 [normal]\n\n### Fixed\n- x\n"
	tests := []struct {
		name    string
		base    string // "" = base file missing
		head    string
		wantErr string
	}{
		{"entry added", base, strings.Replace(base, "- Old entry.\n", "- Old entry.\n- New entry.\n", 1), ""},
		{"untouched", base, base, "was not changed"},
		{"whitespace only", base, strings.Replace(base, "### Fixed\n- Old", "### Fixed\n\n- Old", 1), "was not changed"},
		{"only a released section changed", base, strings.Replace(base, "- x\n", "- y\n", 1), "was not changed"},
		{"minor release cut with highlights", base, cut("0.4.0", 3), ""},
		{"minor release cut with four highlights", base, cut("0.4.0", 4), ""},
		{"minor release cut without highlights", base, cut("0.4.0", 0), "needs 3 or 4 ### Highlights bullets, has 0"},
		{"minor release cut with two highlights", base, cut("0.4.0", 2), "has 2"},
		{"minor release cut with five highlights", base, cut("0.4.0", 5), "has 5"},
		{"patch release cut without highlights", base, cut("0.3.4", 0), ""},
		{"patch release cut with one highlight", base, cut("0.3.4", 1), ""},
		{"unreleased highlights are optional", base, strings.Replace(base, "### Fixed\n- Old entry.\n", "### Highlights\n- One.\n\n### Fixed\n- Old entry.\n- New entry.\n", 1), ""},
		{"old minor release without highlights is not rechecked",
			"# Changelog\n\n## [Unreleased]\n\n## [0.3.0] - 2026-09-01 [normal]\n\n### Fixed\n- x\n",
			"# Changelog\n\n## [Unreleased]\n\n### Fixed\n- y\n\n## [0.3.0] - 2026-09-01 [normal]\n\n### Fixed\n- x\n", ""},
		{"no base changelog yet", "", base, ""},
		{"head has no unreleased", base, "# Changelog\n", "no [Unreleased] section"},
		{"malformed head", base, base + "\n### Removed\n- z\n", "unknown subsection"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			head := writeFile(t, dir, "CHANGELOG.md", tt.head)
			basePath := filepath.Join(dir, "base.md")
			if tt.base != "" {
				writeFile(t, dir, "base.md", tt.base)
			}
			err := run([]string{"check", "-changelog", head, "-base", basePath}, &bytes.Buffer{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
