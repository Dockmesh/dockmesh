//go:build !labkeys

package selfupdate

import "testing"

// TestReleaseBuildsHaveNoLabOverrides runs in every build without the
// labkeys tag and fails if a lab override is ever set outside the tagged
// file.
func TestReleaseBuildsHaveNoLabOverrides(t *testing.T) {
	t.Setenv("DOCKMESH_LAB_RELEASE_BASE", "http://127.0.0.1:1/")
	if labReleaseBase != nil || labAPIBase != nil || labLockWait != nil {
		t.Fatal("a lab override is set in a build without the labkeys tag")
	}
	if got := ReleaseBaseURL(); got != assetPrefix {
		t.Errorf("ReleaseBaseURL() = %q, want %q", got, assetPrefix)
	}
}
