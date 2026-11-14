//go:build !labkeys

package releasesig

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedKeys runs in every build without the labkeys tag, which is
// what the release workflow builds: it must trust the two real keys only.
func TestEmbeddedKeys(t *testing.T) {
	v := Default()
	want := []uint64{0x01A0941E358E1C5A, 0x73BBC751F8715F03}
	if len(v.keys) != len(want) {
		t.Fatalf("got %d keys, want %d", len(v.keys), len(want))
	}
	for i, id := range want {
		if v.keys[i].ID() != id {
			t.Errorf("key %d: id %016X, want %016X", i, v.keys[i].ID(), id)
		}
	}
}

// TestReleaseBuildsHaveNoLabKeys keeps the labkeys tag out of the builds
// that get published.
func TestReleaseBuildsHaveNoLabKeys(t *testing.T) {
	for _, f := range []string{"../../.github/workflows/release.yml", "../../Makefile", "../../Dockerfile"} {
		data, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "labkeys") {
			t.Errorf("%s mentions the labkeys build tag", f)
		}
	}
}
