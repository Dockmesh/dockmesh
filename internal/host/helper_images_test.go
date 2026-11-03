package host

import (
	"testing"

	"github.com/distribution/reference"
)

// Helper images mount volumes with data; a tag alone could be replaced
// on the registry, so each one must carry a digest.
func TestHelperImagesPinned(t *testing.T) {
	for _, img := range []string{helperImage, VolumeTarHelperImage} {
		ref, err := reference.ParseNormalizedNamed(img)
		if err != nil {
			t.Fatalf("%s: %v", img, err)
		}
		if _, ok := ref.(reference.Digested); !ok {
			t.Errorf("%s is not pinned by digest", img)
		}
	}
}
