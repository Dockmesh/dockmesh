// Command relmanifest turns a CHANGELOG.md version section into GitHub
// release notes and release-manifest.json, and checks pull requests for a
// changelog entry. The changelog syntax is documented at the top of
// CHANGELOG.md.
//
//	relmanifest release -version v0.3.4 [-changelog CHANGELOG.md] -notes notes.md [-body body.md] -manifest release-manifest.json
//	relmanifest check -base base-CHANGELOG.md [-changelog CHANGELOG.md]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	changelog := fl.String("changelog", "CHANGELOG.md", "changelog path")
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
	sections, err := parseFile(*changelog)
	if err != nil {
		return err
	}
	s := Find(sections, *version)
	if s == nil || s.Version == unreleased {
		return fmt.Errorf("%s has no section for %s; rename [Unreleased] to the release version before tagging", *changelog, *version)
	}
	if s.empty() {
		return fmt.Errorf("section [%s] has no entries", s.Version)
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
	changelog := fl.String("changelog", "CHANGELOG.md", "changelog path in the PR head")
	base := fl.String("base", "", "changelog from the PR base; a missing file counts as empty")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *base == "" {
		return errors.New("check: -base is required")
	}
	head, err := parseFile(*changelog)
	if err != nil {
		return err
	}
	if Find(head, unreleased) == nil {
		return fmt.Errorf("%s has no [Unreleased] section", *changelog)
	}
	var baseSections []*Section
	if _, statErr := os.Stat(*base); statErr == nil {
		if baseSections, err = parseFile(*base); err != nil {
			return fmt.Errorf("base: %w", err)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	if unreleasedBody(head) == unreleasedBody(baseSections) && !hasNewRelease(head, baseSections) {
		return errors.New("CHANGELOG.md [Unreleased] was not changed; add an entry, or label the PR no-changelog if users won't notice the change")
	}
	fmt.Fprintln(stdout, "changelog: [Unreleased] updated")
	return nil
}

// hasNewRelease reports whether head carries a versioned section that base
// lacks, i.e. the PR cuts a release and moves the Unreleased entries into it.
func hasNewRelease(head, base []*Section) bool {
	for _, s := range head {
		if s.Version != unreleased && Find(base, s.Version) == nil {
			return true
		}
	}
	return false
}

func parseFile(path string) ([]*Section, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the CLI caller
	if err != nil {
		return nil, err
	}
	sections, err := Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sections, nil
}
