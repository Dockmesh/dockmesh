package semver

import "testing"

func TestNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v0.6.0-rc.2", "v0.6.0-rc.1", true},
		{"v0.6.0-rc.1", "v0.6.0-rc.2", false},
		{"v0.6.0", "v0.6.0-rc.9", true},
		{"v0.6.0-rc.9", "v0.6.0", false},
		{"v0.6.0-rc.10", "v0.6.0-rc.9", true},
		{"v0.6.1-rc.1", "v0.6.0", true},
		{"v0.6.0", "v0.6.0", false},
		{"v0.5.10", "v0.5.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"0.6.0", "v0.5.3", true},
		{"v0.6.0+build.7", "v0.6.0", false},
		{"v0.6.0-rc.1", "v0.6.0-beta.2", true},
		{"v0.6.0-rc.1.1", "v0.6.0-rc.1", true},
		{"dev", "v0.5.3", false},
		{"v0.6.0", "dev", false},
		{"v0.6", "v0.5.0", false},
		{"v0.6.0-", "v0.5.0", false},
		{"v0.6.0-rc..1", "v0.5.0", false},
		{"v0.6.0-rc_1", "v0.5.0", false},
		{"v01234567890.0.0", "v0.5.0", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestPrerelease(t *testing.T) {
	tests := []struct {
		v    string
		want bool
	}{
		{"v0.6.0-rc.1", true},
		{"v0.6.0", false},
		{"v0.6.0+meta", false},
	}
	for _, tt := range tests {
		v, ok := Parse(tt.v)
		if !ok {
			t.Fatalf("Parse(%q) failed", tt.v)
		}
		if got := v.Prerelease(); got != tt.want {
			t.Errorf("%q.Prerelease() = %v, want %v", tt.v, got, tt.want)
		}
	}
}
