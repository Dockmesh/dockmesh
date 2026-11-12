package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsLatest(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		published []string
		want      bool
	}{
		{"patch on old branch after newer minor", "v0.5.4", []string{"v0.6.0", "v0.5.3", "v0.5.2"}, false},
		{"patch on old branch after newer patch", "v0.5.4", []string{"v0.6.1", "v0.6.0", "v0.5.3"}, false},
		{"patch before next minor", "v0.5.4", []string{"v0.5.3", "v0.5.2"}, true},
		{"new minor", "v0.6.0", []string{"v0.5.4", "v0.5.3"}, true},
		{"patch of the current minor", "v0.6.1", []string{"v0.6.0", "v0.5.4"}, true},
		{"re-run of the latest release", "v0.6.0", []string{"v0.6.0", "v0.5.3"}, true},
		{"re-run of an older release", "v0.5.3", []string{"v0.6.0", "v0.5.3"}, false},
		{"numeric not lexical order", "v0.10.0", []string{"v0.9.9", "v0.2.0"}, true},
		{"numeric order the other way", "v0.9.9", []string{"v0.10.0"}, false},
		{"first release", "v1.0.0", nil, true},
		{"newer pre-release does not count", "v0.5.4", []string{"v0.6.0-rc.1", "v0.5.3"}, true},
		{"pre-release is never latest", "v0.6.0-rc.1", []string{"v0.5.3"}, false},
		{"pre-release of a far newer major is never latest", "v9.0.0-rc.1", []string{"v0.5.3"}, false},
		{"first stable after its own pre-release", "v0.6.0", []string{"v0.6.0-rc.2", "v0.6.0-rc.1", "v0.5.3"}, true},
		{"untagged version is never latest", "0.5.4", []string{"v0.5.3"}, false},
		{"junk published tags are ignored", "v0.5.4", []string{"nightly", "v0.5.3"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why := isLatest(tt.version, tt.published)
			if got != tt.want {
				t.Errorf("isLatest(%q, %v) = %t (%s), want %t", tt.version, tt.published, got, why, tt.want)
			}
		})
	}
}

func TestLatestCommand(t *testing.T) {
	dir := t.TempDir()
	published := writeFile(t, dir, "published.txt", "v0.6.0\n\nv0.5.3\n")
	tests := []struct {
		version string
		want    string
	}{
		{"v0.5.4", "false"},
		{"v0.6.1", "true"},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			var out bytes.Buffer
			if err := run([]string{"latest", "-version", tt.version, "-published", published}, &out); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(out.String()); got != tt.want {
				t.Errorf("latest %s = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestLatestCommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no version", []string{"latest", "-published", "x"}},
		{"no published file flag", []string{"latest", "-version", "v0.5.4"}},
		{"missing published file", []string{"latest", "-version", "v0.5.4", "-published", "/nonexistent/published.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(tt.args, &bytes.Buffer{}); err == nil {
				t.Error("want error")
			}
		})
	}
}
