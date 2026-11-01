// Command relmanifest turns a CHANGELOG.md version section into GitHub
// release notes and release-manifest.json, and checks the changie fragments
// of pull requests. The changelog syntax is documented at the top of
// CHANGELOG.md, which changie generates from .changes/.
//
//	relmanifest release -version v0.3.4 [-changelog CHANGELOG.md] -notes notes.md [-body body.md] -manifest release-manifest.json
//	relmanifest check -base base-CHANGELOG.md -changed changed.txt [-changelog CHANGELOG.md] [-fragments .changes/unreleased]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/changelog"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "relmanifest:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: relmanifest release|check [flags]")
	}
	switch args[0] {
	case "release":
		return runRelease(args[1:], stdout)
	case "check":
		return runCheck(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q (want release or check)", args[0])
	}
}

func runRelease(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("release", flag.ContinueOnError)
	changelogPath := fl.String("changelog", "CHANGELOG.md", "changelog path")
	version := fl.String("version", "", "version or tag to release, e.g. v0.3.4")
	notesOut := fl.String("notes", "", "write release notes (Markdown) here")
	bodyOut := fl.String("body", "", "write the short release body (Markdown) here")
	manifestOut := fl.String("manifest", "", "write release-manifest.json here")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *version == "" || *notesOut == "" || *manifestOut == "" {
		return errors.New("release: -version, -notes and -manifest are required")
	}
	sections, err := parseFile(*changelogPath)
	if err != nil {
		return err
	}
	s := changelog.Find(sections, *version)
	if s == nil || s.Version == changelog.Unreleased {
		return fmt.Errorf("%s has no section for %s; rename [Unreleased] to the release version before tagging", *changelogPath, *version)
	}
	if s.Empty() {
		return fmt.Errorf("section [%s] has no entries", s.Version)
	}
	if err := s.CheckHighlights(); err != nil {
		return err
	}
	manifest, err := ManifestJSON(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*notesOut, []byte(RenderNotes(s)), 0o644); err != nil { //nolint:gosec // release artifact, not secret
		return err
	}
	if *bodyOut != "" {
		if err := os.WriteFile(*bodyOut, []byte(RenderBody(s)), 0o644); err != nil { //nolint:gosec // release artifact, not secret
			return err
		}
	}
	if err := os.WriteFile(*manifestOut, manifest, 0o644); err != nil { //nolint:gosec // release artifact, not secret
		return err
	}
	fmt.Fprintf(stdout, "%s (%s): %s, %s\n", s.Version, s.Severity, *notesOut, *manifestOut)
	return nil
}

func runCheck(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("check", flag.ContinueOnError)
	changelogPath := fl.String("changelog", "CHANGELOG.md", "changelog path in the PR head")
	base := fl.String("base", "", "changelog from the PR base; a missing file counts as empty")
	fragDir := fl.String("fragments", ".changes/unreleased", "changie fragments directory in the PR head")
	changed := fl.String("changed", "", "file listing the paths the PR adds or modifies, one per line")
	optional := fl.Bool("optional", false, "validate fragments but don't require one (PR labelled no-changelog)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *base == "" || (*changed == "" && !*optional) {
		return errors.New("check: -base and -changed are required")
	}
	frags, err := checkFragments(*fragDir)
	if err != nil {
		return err
	}
	head, err := parseFile(*changelogPath)
	if err != nil {
		return err
	}
	if changelog.Find(head, changelog.Unreleased) == nil {
		return fmt.Errorf("%s has no [Unreleased] section", *changelogPath)
	}
	var baseSections []*changelog.Section
	if _, statErr := os.Stat(*base); statErr == nil {
		if baseSections, err = parseFile(*base); err != nil {
			return fmt.Errorf("base: %w", err)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	if baseSections != nil {
		for _, s := range newReleases(head, baseSections) {
			if err := s.CheckHighlights(); err != nil {
				return err
			}
		}
	}
	if hasNewRelease(head, baseSections) {
		fmt.Fprintln(stdout, "changelog: release section added")
		return nil
	}
	if *optional {
		fmt.Fprintf(stdout, "changelog: %d fragment(s) valid, none required\n", len(frags))
		return nil
	}
	added, err := changedFragments(*changed, frags)
	if err != nil {
		return err
	}
	if added == 0 {
		return fmt.Errorf("no changelog fragment added under %s; run changie new, or label the PR no-changelog if users won't notice the change", *fragDir)
	}
	fmt.Fprintf(stdout, "changelog: %d fragment(s) added or changed\n", added)
	return nil
}

type fragment struct {
	Kind string `yaml:"kind"`
	Body string `yaml:"body"`
}

// checkFragments parses each fragment in dir as its own bullet, then all of
// them together to catch clashes such as duplicate upgrade ids. It returns
// the file names.
func checkFragments(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	names := map[string]bool{}
	byKind := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // repository file
		if err != nil {
			return nil, err
		}
		var f fragment
		if err := yaml.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		body := strings.TrimSpace(f.Body)
		switch {
		case !changelog.IsSubsection(f.Kind):
			return nil, fmt.Errorf("%s: unknown kind %q", e.Name(), f.Kind)
		case body == "":
			return nil, fmt.Errorf("%s: body is empty", e.Name())
		case strings.ContainsAny(body, "\r\n"):
			return nil, fmt.Errorf("%s: body must be one line", e.Name())
		}
		if _, err := changelog.Parse("## [Unreleased]\n### " + f.Kind + "\n- " + body + "\n"); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		names[e.Name()] = true
		byKind[f.Kind] = append(byKind[f.Kind], body)
	}
	var all strings.Builder
	all.WriteString("## [Unreleased]\n")
	for kind, bodies := range byKind {
		all.WriteString("### " + kind + "\n")
		for _, b := range bodies {
			all.WriteString("- " + b + "\n")
		}
	}
	if _, err := changelog.Parse(all.String()); err != nil {
		return nil, fmt.Errorf("fragments: %w", err)
	}
	return names, nil
}

// changedFragments counts the paths listed in the file at path that name
// one of the fragments.
func changedFragments(path string, frags map[string]bool) (int, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path from the CLI caller
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && frags[filepath.Base(line)] {
			n++
		}
	}
	return n, nil
}

// hasNewRelease reports whether head carries a versioned section that base
// lacks, i.e. the PR cuts a release.
func hasNewRelease(head, base []*changelog.Section) bool {
	return len(newReleases(head, base)) > 0
}

func newReleases(head, base []*changelog.Section) []*changelog.Section {
	var out []*changelog.Section
	for _, s := range head {
		if s.Version != changelog.Unreleased && changelog.Find(base, s.Version) == nil {
			out = append(out, s)
		}
	}
	return out
}

func parseFile(path string) ([]*changelog.Section, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the CLI caller
	if err != nil {
		return nil, err
	}
	sections, err := changelog.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sections, nil
}
