//go:build !labkeys

package selfupdate

import "testing"

// TestReleaseBuildsHaveNoLabOverrides runs in every build without the
// labkeys tag: the lab environment variables must not move downloads,
// the release lookup or the stack wait of a published binary.
func TestReleaseBuildsHaveNoLabOverrides(t *testing.T) {
	t.Setenv("DOCKMESH_LAB_RELEASE_BASE", "http://127.0.0.1:1/")
	t.Setenv("DOCKMESH_LAB_RELEASE_API", "http://127.0.0.1:1/")
	t.Setenv("DOCKMESH_LAB_UPDATE_LOCK_WAIT", "1s")
	if labReleaseBase != nil || labAPIBase != nil || labLockWait != nil {
		t.Fatal("a lab override is set in a build without the labkeys tag")
	}
	if got := ReleaseBaseURL(); got != assetPrefix {
		t.Errorf("ReleaseBaseURL() = %q, want %q", got, assetPrefix)
	}
}
