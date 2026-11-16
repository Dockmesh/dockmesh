//go:build labkeys

package labtime

import (
	"testing"
	"time"
)

func TestLabDay(t *testing.T) {
	const d = 24 * time.Hour
	tests := []struct {
		env         string
		in          time.Duration
		scale, tick time.Duration
	}{
		{"", 30 * d, 30 * d, 30 * d},
		{"garbage", 30 * d, 30 * d, 30 * d},
		{"-1s", 30 * d, 30 * d, 30 * d},
		{"48h", 30 * d, 30 * d, 30 * d},
		{"1s", 30 * d, 30 * time.Second, 30 * time.Second},
		{"1s", 365 * d, 365 * time.Second, 365 * time.Second},
		{"1s", 12 * time.Hour, 500 * time.Millisecond, time.Second},
		{"1s", 100 * time.Millisecond, 100 * time.Millisecond / 86400, 100 * time.Millisecond},
		{"1m", 7 * d, 7 * time.Minute, 7 * time.Minute},
	}
	for _, tc := range tests {
		ld, _ := time.ParseDuration(tc.env)
		day = func() time.Duration { return ld }
		if got := Scale(tc.in); got != tc.scale {
			t.Errorf("%q: Scale(%v) = %v, want %v", tc.env, tc.in, got, tc.scale)
		}
		if got := Tick(tc.in); got != tc.tick {
			t.Errorf("%q: Tick(%v) = %v, want %v", tc.env, tc.in, got, tc.tick)
		}
	}
}
