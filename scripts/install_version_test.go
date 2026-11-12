package scripts

import (
	"os/exec"
	"strings"
	"testing"
)

func runVersionHelper(t *testing.T, call string, args ...string) (string, error) {
	t.Helper()
	script := "set -euo pipefail\ndie() { echo \"$*\" >&2; exit 1; }\n" + envHelpers(t) + "\n" + call + "\n"
	out, err := exec.Command("bash", append([]string{"-c", script, "install-test"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestInstallParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{"no arguments keep the default", nil, "unset", ""},
		{"separate value", []string{"--version", "v0.6.0-rc.1"}, "v0.6.0-rc.1", ""},
		{"inline value", []string{"--version=v0.6.0-rc.2"}, "v0.6.0-rc.2", ""},
		{"missing value", []string{"--version"}, "", "needs a release tag"},
		{"unknown argument", []string{"--channel", "testing"}, "", "unknown argument: --channel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runVersionHelper(t, `parse_args "$@"; echo "${DOCKMESH_VERSION:-unset}"`, tt.args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(out, tt.wantErr) {
					t.Fatalf("err = %v, output %q, want %q", err, out, tt.wantErr)
				}
				return
			}
			if err != nil || out != tt.want {
				t.Fatalf("got %q (err %v), want %q", out, err, tt.want)
			}
		})
	}
}

func TestInstallValidTag(t *testing.T) {
	for tag, want := range map[string]bool{
		"v0.5.3":            true,
		"v0.6.0-rc.1":       true,
		"v10.20.30":         true,
		"0.5.3":             false,
		"latest":            false,
		"v0.6":              false,
		"v0.6.0-rc.1;id":    false,
		"v0.6.0/../../evil": false,
		"":                  false,
	} {
		_, err := runVersionHelper(t, `valid_tag "$1"`, tag)
		if got := err == nil; got != want {
			t.Errorf("valid_tag(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestInstallVersionOlder(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v0.5.3", "v0.6.0-rc.1", true},
		{"v0.6.0", "v0.6.0-rc.2", false},
		{"v0.6.0-rc.2", "v0.6.0-rc.1", false},
		{"v0.6.1", "v0.6.0-rc.1", false},
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v0.9.0", false},
		{"v1.0.0", "v0.99.99", false},
		{"v0.5.3", "v0.5.3", false},
	}
	for _, tt := range tests {
		_, err := runVersionHelper(t, `version_older "$1" "$2"`, tt.a, tt.b)
		if got := err == nil; got != tt.want {
			t.Errorf("version_older(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
