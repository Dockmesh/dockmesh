package backup

import (
	"testing"

	"github.com/distribution/reference"
)

func TestHelperImagePinned(t *testing.T) {
	ref, err := reference.ParseNormalizedNamed(helperImage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ref.(reference.Digested); !ok {
		t.Fatalf("%s is not pinned by digest", helperImage)
	}
}
