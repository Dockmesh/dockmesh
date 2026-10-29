// Command docscheck validates the user documentation in docs/site: every
// page is listed in nav.json exactly once, every nav entry exists, each page
// carries title and description frontmatter, and relative links resolve.
//
//	docscheck [-root docs/site]
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

type nav struct {
	Version  int          `json:"version"`
	Sections []navSection `json:"sections"`
}

type navSection struct {
	Title string    `json:"title"`
	Items []navItem `json:"items"`
}

type navItem struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

func main() {
	root := flag.String("root", "docs/site", "documentation root")
	flag.Parse()
	problems, err := check(os.DirFS(*root))
	if err != nil {
		fmt.Fprintln(os.Stderr, "docscheck:", err)
		os.Exit(1)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

// check returns one line per problem; the error is reserved for an
// unreadable tree or nav.json.
func check(fsys fs.FS) ([]string, error) {
	raw, err := fs.ReadFile(fsys, "nav.json")
	if err != nil {
		return nil, err
	}
	var n nav
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("nav.json: %w", err)
	}

	var problems []string
	if n.Version != 1 {
		problems = append(problems, fmt.Sprintf("nav.json: version %d, want 1", n.Version))
	}

	listed := map[string]int{}
	for _, s := range n.Sections {
		if s.Title == "" {
			problems = append(problems, "nav.json: section without title")
		}
		if len(s.Items) == 0 {
			problems = append(problems, fmt.Sprintf("nav.json: section %q has no items", s.Title))
		}
		for _, it := range s.Items {
			if it.Title == "" {
				problems = append(problems, fmt.Sprintf("nav.json: %s: item without title", it.Path))
			}
			if !fs.ValidPath(it.Path) || !strings.HasSuffix(it.Path, ".md") {
				problems = append(problems, fmt.Sprintf("nav.json: invalid path %q", it.Path))
				continue
			}
			listed[it.Path]++
		}
	}

	pages, err := markdownPages(fsys)
	if err != nil {
		return nil, err
	}
	onDisk := map[string]bool{}
	for _, p := range pages {
		onDisk[p] = true
		switch listed[p] {
		case 0:
			problems = append(problems, fmt.Sprintf("%s: not listed in nav.json", p))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("%s: listed %d times in nav.json", p, listed[p]))
		}
	}
	for _, p := range sortedKeys(listed) {
		if !onDisk[p] {
			problems = append(problems, fmt.Sprintf("nav.json: %s does not exist", p))
		}
	}

	for _, p := range pages {
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		problems = append(problems, checkPage(fsys, p, body)...)
	}
	return problems, nil
}

// markdownPages skips the root README because it documents the format and
// is not part of the site.
func markdownPages(fsys fs.FS) ([]string, error) {
	var pages []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == "assets" {
			return fs.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") && p != "README.md" {
			pages = append(pages, p)
		}
		return nil
	})
	sort.Strings(pages)
	return pages, err
}

var (
	allowedFrontmatter = map[string]bool{"title": true, "description": true}
	linkRe             = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	schemeRe           = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
)

func checkPage(fsys fs.FS, p string, body []byte) []string {
	fm, rest, err := splitFrontmatter(body)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", p, err)}
	}
	var problems []string
	for _, k := range []string{"title", "description"} {
		if strings.TrimSpace(fm[k]) == "" {
			problems = append(problems, fmt.Sprintf("%s: frontmatter %s missing", p, k))
		}
	}
	for _, k := range sortedKeys(fm) {
		if !allowedFrontmatter[k] {
			problems = append(problems, fmt.Sprintf("%s: unexpected frontmatter key %q", p, k))
		}
	}

	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(rest))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range linkRe.FindAllStringSubmatch(stripCodeSpans(line), -1) {
			if msg := checkLink(fsys, p, m[1]); msg != "" {
				problems = append(problems, fmt.Sprintf("%s: link %s: %s", p, m[1], msg))
			}
		}
	}
	return problems
}

func checkLink(fsys fs.FS, page, target string) string {
	if schemeRe.MatchString(target) || strings.HasPrefix(target, "#") {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return "absolute link, use a relative path"
	}
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target = target[:i]
	}
	resolved := path.Clean(path.Join(path.Dir(page), target))
	if !fs.ValidPath(resolved) {
		return "points outside docs/site"
	}
	if _, err := fs.Stat(fsys, resolved); err != nil {
		return "target does not exist"
	}
	return ""
}

func splitFrontmatter(body []byte) (map[string]string, []byte, error) {
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(body, []byte("---\n")) {
		return nil, nil, errors.New("no frontmatter")
	}
	end := bytes.Index(body[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, nil, errors.New("unterminated frontmatter")
	}
	fm := map[string]string{}
	for _, line := range strings.Split(string(body[4:4+end]), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, fmt.Errorf("frontmatter line %q is not key: value", line)
		}
		fm[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return fm, body[4+end+5:], nil
}

func stripCodeSpans(line string) string {
	var b strings.Builder
	in := false
	for _, r := range line {
		if r == '`' {
			in = !in
			continue
		}
		if !in {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
