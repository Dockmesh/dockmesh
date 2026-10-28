package main

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
	Added          []string
	Changed        []string
	Fixed          []string
	Security       []string
	Advisories     []Advisory
	Actions        []Action

	raw []string
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

const unreleased = "Unreleased"

var (
	releaseHeader    = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\] - (\d{4}-\d{2}-\d{2}) \[(security|recommended|normal)\]$`)
	unreleasedHeader = regexp.MustCompile(`^## \[Unreleased\]$`)
	minUpgradeLine   = regexp.MustCompile(`^Minimum upgrade from: (\d+\.\d+\.\d+)$`)
	linkRefLine      = regexp.MustCompile(`^\[[^\]]+\]: \S+$`)
	advisoryBullet   = regexp.MustCompile(`^(GHSA(?:-[23456789cfghjmpqrvwx]{4}){3}) \((critical|high|moderate|low)\): (.+)$`)
	actionBullet     = regexp.MustCompile(`^\[([a-z0-9][a-z0-9-]*), (all|admins)\] (.+)$`)
)

var subsections = map[string]bool{
	"Added": true, "Changed": true, "Fixed": true, "Security": true, "Upgrade notes": true,
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
				cur = &Section{Version: unreleased}
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
		cur.raw = append(cur.raw, line)
		if linkRefLine.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			if err := flushSub(); err != nil {
				return nil, err
			}
			sub = strings.TrimPrefix(line, "### ")
			if !subsections[sub] {
				return nil, fmt.Errorf("line %d: unknown subsection %q (want Added, Changed, Fixed, Security or Upgrade notes)", lineNo, sub)
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
			m := actionBullet.FindStringSubmatch(b)
			if m == nil {
				return fmt.Errorf("[%s] upgrade note %q must start with \"[id, all|admins] \"", s.Version, b)
			}
			for _, a := range s.Actions {
				if a.ID == m[1] {
					return fmt.Errorf("[%s] duplicate upgrade action id %q", s.Version, m[1])
				}
			}
			s.Actions = append(s.Actions, Action{ID: m[1], Audience: m[2], Text: m[3]})
		}
	}
	return nil
}

func (s *Section) empty() bool {
	return len(s.Added)+len(s.Changed)+len(s.Fixed)+len(s.Security)+len(s.Advisories)+len(s.Actions) == 0
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

// unreleasedBody is the Unreleased section's lines with blank lines dropped,
// so whitespace-only edits don't count as a changelog entry.
func unreleasedBody(sections []*Section) string {
	s := Find(sections, unreleased)
	if s == nil {
		return ""
	}
	var keep []string
	for _, l := range s.raw {
		if l != "" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
