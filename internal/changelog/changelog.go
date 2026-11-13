// Package changelog parses CHANGELOG.md, whose syntax is documented at
// the top of that file. The release tooling renders notes and the
// release manifest from it; the server reads its own version's section.
package changelog

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// Section is one "## [...]" block of CHANGELOG.md.
type Section struct {
	Version        string // "Unreleased" or a semver without the leading "v"
	Date           string
	Severity       string
	Summary        string
	MinUpgradeFrom string
	Highlights     []string
	Added          []string
	Changed        []string
	Fixed          []string
	Security       []string
	Advisories     []Advisory
	Actions        []Action
}

type Advisory struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
}

type Action struct {
	ID       string `json:"id"`
	Audience string `json:"audience"`
	Text     string `json:"text"`
}

// Unreleased is the Version of the "## [Unreleased]" section.
const Unreleased = "Unreleased"

var (
	releaseHeader    = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\] - (\d{4}-\d{2}-\d{2}) \[(security|recommended|normal)\]$`)
	unreleasedHeader = regexp.MustCompile(`^## \[Unreleased\]$`)
	minUpgradeLine   = regexp.MustCompile(`^Minimum upgrade from: (\d+\.\d+\.\d+)$`)
	linkRefLine      = regexp.MustCompile(`^\[[^\]]+\]: \S+$`)
	advisoryBullet   = regexp.MustCompile(`^(GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}) \((critical|high|moderate|low)\): (.+)$`)
	actionBullet     = regexp.MustCompile(`^(?:\*\*([A-Za-z]+):\*\* )?(.+) <!-- ([a-z0-9][a-z0-9-]*) -->$`)
	actionID         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	oldActionBullet  = regexp.MustCompile(`^\[([a-z0-9][a-z0-9-]*), (all|admins)\] (.+)$`)
)

// audienceLabels names the audiences narrower than "all"; an upgrade note
// for everyone carries no label.
var audienceLabels = map[string]string{"admins": "Admins"}

// AudienceLabel is the label an upgrade note for audience starts with, or ""
// for everyone.
func AudienceLabel(audience string) string { return audienceLabels[audience] }

// FormatAction is a's text as it appears in CHANGELOG.md and the release
// notes: the audience label, if any, then the text.
func FormatAction(a Action) string {
	if l := AudienceLabel(a.Audience); l != "" {
		return "**" + l + ":** " + a.Text
	}
	return a.Text
}

// ActionBullet is a's CHANGELOG.md bullet without the leading "- "; the id
// sits in an HTML comment so rendered markdown doesn't show it.
func ActionBullet(a Action) string { return FormatAction(a) + " <!-- " + a.ID + " -->" }

// NewAction validates the fields an upgrade-note fragment stores.
func NewAction(id, audience, text string) (Action, error) {
	switch {
	case !actionID.MatchString(id):
		return Action{}, fmt.Errorf("upgrade note id %q must be lower-case kebab-case", id)
	case audience != "all" && AudienceLabel(audience) == "":
		return Action{}, fmt.Errorf("upgrade note audience %q must be all or admins", audience)
	case strings.TrimSpace(text) == "":
		return Action{}, fmt.Errorf("upgrade note %q has no text", id)
	}
	return Action{ID: id, Audience: audience, Text: text}, nil
}

// parseAction also reads the "[id, audience] text" form the changelog used
// up to 0.5.1, since checks parse the base branch's CHANGELOG.md as well.
func parseAction(b string) (Action, bool) {
	if m := oldActionBullet.FindStringSubmatch(b); m != nil {
		return Action{ID: m[1], Audience: m[2], Text: m[3]}, true
	}
	m := actionBullet.FindStringSubmatch(b)
	if m == nil {
		return Action{}, false
	}
	audience := "all"
	if m[1] != "" {
		audience = ""
		for a, l := range audienceLabels {
			if l == m[1] {
				audience = a
			}
		}
		if audience == "" {
			return Action{}, false
		}
	}
	return Action{ID: m[3], Audience: audience, Text: m[2]}, true
}

var subsections = map[string]bool{
	"Highlights": true, "Added": true, "Changed": true, "Fixed": true, "Security": true, "Upgrade notes": true,
}

// Parse reads a whole changelog. Text before the first "## " header is free
// preamble; everything after it must follow the documented syntax.
func Parse(src string) ([]*Section, error) {
	var (
		out     []*Section
		cur     *Section
		sub     string
		bullets []string // finished bullets of the current subsection
		item    []string // lines of the bullet being read
		summary []string
		lineNo  int
	)
	flushItem := func() {
		if len(item) > 0 {
			bullets = append(bullets, strings.Join(item, " "))
			item = nil
		}
	}
	flushSub := func() error {
		flushItem()
		if cur == nil || sub == "" {
			return nil
		}
		if err := cur.add(sub, bullets); err != nil {
			return err
		}
		bullets = nil
		return nil
	}
	flushSection := func() error {
		if err := flushSub(); err != nil {
			return err
		}
		if cur != nil {
			cur.Summary = strings.TrimSpace(strings.Join(summary, "\n"))
			out = append(out, cur)
		}
		summary = nil
		sub = ""
		return nil
	}

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.HasPrefix(line, "## ") {
			if err := flushSection(); err != nil {
				return nil, err
			}
			switch {
			case unreleasedHeader.MatchString(line):
				cur = &Section{Version: Unreleased}
			case releaseHeader.MatchString(line):
				m := releaseHeader.FindStringSubmatch(line)
				cur = &Section{Version: m[1], Date: m[2], Severity: m[3]}
			default:
				return nil, fmt.Errorf("line %d: malformed version header %q (want \"## [X.Y.Z] - YYYY-MM-DD [security|recommended|normal]\" or \"## [Unreleased]\")", lineNo, line)
			}
			for _, s := range out {
				if s.Version == cur.Version {
					return nil, fmt.Errorf("line %d: duplicate section %q", lineNo, cur.Version)
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		if linkRefLine.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			if err := flushSub(); err != nil {
				return nil, err
			}
			sub = strings.TrimPrefix(line, "### ")
			if !subsections[sub] {
				return nil, fmt.Errorf("line %d: unknown subsection %q (want Highlights, Added, Changed, Fixed, Security or Upgrade notes)", lineNo, sub)
			}
			continue
		}
		if sub == "" {
			if m := minUpgradeLine.FindStringSubmatch(line); m != nil {
				cur.MinUpgradeFrom = m[1]
				continue
			}
			summary = append(summary, line)
			continue
		}
		switch {
		case line == "":
			flushItem()
		case strings.HasPrefix(line, "- "):
			flushItem()
			item = []string{strings.TrimSpace(line[2:])}
		case len(item) > 0 && (line[0] == ' ' || line[0] == '\t'):
			item = append(item, strings.TrimSpace(line))
		default:
			return nil, fmt.Errorf("line %d: expected a \"- \" bullet under %q, got %q", lineNo, sub, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := flushSection(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Section) add(sub string, bullets []string) error {
	for _, b := range bullets {
		switch sub {
		case "Highlights":
			s.Highlights = append(s.Highlights, b)
		case "Added":
			s.Added = append(s.Added, b)
		case "Changed":
			s.Changed = append(s.Changed, b)
		case "Fixed":
			s.Fixed = append(s.Fixed, b)
		case "Security":
			if m := advisoryBullet.FindStringSubmatch(b); m != nil {
				s.Advisories = append(s.Advisories, Advisory{ID: m[1], Severity: m[2], Title: m[3]})
			} else {
				s.Security = append(s.Security, b)
			}
		case "Upgrade notes":
			act, ok := parseAction(b)
			if !ok {
				return fmt.Errorf("[%s] upgrade note %q must be \"[**Admins:** ]text <!-- id -->\"", s.Version, b)
			}
			for _, a := range s.Actions {
				if a.ID == act.ID {
					return fmt.Errorf("[%s] duplicate upgrade action id %q", s.Version, act.ID)
				}
			}
			s.Actions = append(s.Actions, act)
		}
	}
	return nil
}

// Empty reports whether the section has no change entries. Highlights
// alone don't count: they summarise entries listed elsewhere.
func (s *Section) Empty() bool {
	return len(s.Added)+len(s.Changed)+len(s.Fixed)+len(s.Security)+len(s.Advisories)+len(s.Actions) == 0
}

// CheckHighlights reports an error when s is a minor release (X.Y.0)
// without three or four Highlights. Patch releases and Unreleased may
// leave them out.
func (s *Section) CheckHighlights() error {
	if !strings.HasSuffix(strings.SplitN(s.Version, "-", 2)[0], ".0") {
		return nil
	}
	if n := len(s.Highlights); n < 3 || n > 4 {
		return fmt.Errorf("[%s] is a minor release and needs 3 or 4 ### Highlights bullets, has %d", s.Version, n)
	}
	return nil
}

// Find returns the section for version, accepting a leading "v" as in git tags.
func Find(sections []*Section, version string) *Section {
	v := strings.TrimPrefix(version, "v")
	for _, s := range sections {
		if s.Version == v {
			return s
		}
	}
	return nil
}

// Manifest is release-manifest.json, the machine-readable form of a
// release section published with every release.
type Manifest struct {
	Schema         int        `json:"schema"`
	Version        string     `json:"version"`
	Date           string     `json:"date"`
	Severity       string     `json:"severity"`
	Summary        string     `json:"summary"`
	Highlights     []string   `json:"highlights"`
	Changes        Changes    `json:"changes"`
	Advisories     []Advisory `json:"advisories"`
	Actions        []Action   `json:"actions"`
	MinUpgradeFrom string     `json:"min_upgrade_from"`
	Prerelease     bool       `json:"prerelease"`
}

// Changes holds a manifest's entries by subsection.
type Changes struct {
	Added    []string `json:"added"`
	Changed  []string `json:"changed"`
	Fixed    []string `json:"fixed"`
	Security []string `json:"security"`
}

// NonNil returns s, or an empty slice when s is nil, so it encodes as [].
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// BuildManifest converts a section to its manifest.
func BuildManifest(s *Section) Manifest {
	return Manifest{
		Schema:     1,
		Version:    s.Version,
		Date:       s.Date,
		Severity:   s.Severity,
		Summary:    s.Summary,
		Highlights: NonNil(s.Highlights),
		Changes: Changes{
			Added:    NonNil(s.Added),
			Changed:  NonNil(s.Changed),
			Fixed:    NonNil(s.Fixed),
			Security: NonNil(s.Security),
		},
		Advisories:     NonNil(s.Advisories),
		Actions:        NonNil(s.Actions),
		MinUpgradeFrom: s.MinUpgradeFrom,
		Prerelease:     s.Prerelease(),
	}
}

// Prerelease reports whether s is a pre-release such as 0.6.0-rc.1.
func (s *Section) Prerelease() bool { return strings.Contains(s.Version, "-") }

// IsSubsection reports whether name is a valid "### " subsection title.
func IsSubsection(name string) bool { return subsections[name] }
