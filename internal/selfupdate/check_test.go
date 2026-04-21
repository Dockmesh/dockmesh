package selfupdate

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.1", "v0.1.1", false},
		{"v0.1.2", "v0.1.1", false},
		{"0.1.0", "0.2.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"v0.1.0", "v1.0.0", true},

		// Non-release current versions should never trigger an update.
		{"dev", "v0.1.1", false},
		{"", "v0.1.1", false},
		{"unknown", "v0.1.1", false},

		// Empty latest is "no check yet".
		{"v0.1.0", "", false},

		// Pre-release suffix on current should compare the base version.
		{"v0.1.0-rc1", "v0.1.0", false},
		{"v0.1.0-rc1", "v0.1.1", true},
	}
	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}
