package config

import (
	"testing"
	"time"
)

func TestParseEnrollTokenTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, true},
		{"2h", 2 * time.Hour, true},
		{"720h", 720 * time.Hour, true},
		{"30s", 0, false},
		{"721h", 0, false},
		{"-1h", 0, false},
		{"tomorrow", 0, false},
	}
	for _, c := range cases {
		got, err := parseEnrollTokenTTL(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("parseEnrollTokenTTL(%q) = %v, %v; want %v ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}
