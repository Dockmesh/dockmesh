//go:build !labkeys

package labtime

import (
	"testing"
	"time"
)

// TestReleaseBuildsIgnoreLabDay runs in every build without the labkeys
// tag and fails if the hook is ever set outside the tagged file.
func TestReleaseBuildsIgnoreLabDay(t *testing.T) {
	t.Setenv("DOCKMESH_LAB_DAY", "1s")
	if day != nil {
		t.Fatal("day is set in a build without the labkeys tag")
	}
	for _, d := range []time.Duration{time.Hour, 30 * 24 * time.Hour, 365 * 24 * time.Hour} {
		if got := Scale(d); got != d {
			t.Errorf("Scale(%v) = %v, want it unchanged", d, got)
		}
		if got := Tick(d); got != d {
			t.Errorf("Tick(%v) = %v, want it unchanged", d, got)
		}
	}
}
