package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var stableTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// parseStable returns [major, minor, patch] for a vX.Y.Z tag; pre-releases
// and anything else are not stable and never become latest.
func parseStable(tag string) ([3]int, bool) {
	m := stableTag.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// isLatest reports whether version may be marked latest: a patch tagged on
// an older release branch after a newer minor is out must not take over
// GitHub's latest release, which get.dockmesh.dev, install.sh and the update
// check all follow.
func isLatest(version string, published []string) (bool, string) {
	v, ok := parseStable(version)
	if !ok {
		return false, version + " is not a stable vX.Y.Z tag"
	}
	for _, p := range published {
		pv, ok := parseStable(p)
		if ok && compareVersions(pv, v) > 0 {
			return false, "newer release " + p + " is published"
		}
	}
	return true, version + " is the highest stable release"
}

func runLatest(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("latest", flag.ContinueOnError)
	version := fl.String("version", "", "tag being released, e.g. v0.5.4")
	publishedPath := fl.String("published", "", "file with the tags of the published releases, one per line")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *version == "" || *publishedPath == "" {
		return errors.New("latest: -version and -published are required")
	}
	f, err := os.Open(*publishedPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var published []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			published = append(published, t)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	latest, why := isLatest(*version, published)
	fmt.Fprintf(os.Stderr, "latest=%t: %s\n", latest, why)
	fmt.Fprintf(stdout, "%t\n", latest)
	return nil
}
