//go:build labkeys

package releasesig

import "testing"

func TestLabBuildTrustsLabKey(t *testing.T) {
	v := Default()
	if len(v.keys) != 3 {
		t.Fatalf("got %d keys, want the two release keys and the lab key", len(v.keys))
	}
}
