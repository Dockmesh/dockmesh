package compose

import (
	"testing"

	"github.com/distribution/reference"
)

// The bind-copy helper gets stack data mounted; a tag alone could be
// replaced on the registry.
func TestBindCopyHelperPinned(t *testing.T) {
	ref, err := reference.ParseNormalizedNamed(bindCopyHelperImage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ref.(reference.Digested); !ok {
		t.Fatalf("%s is not pinned by digest", bindCopyHelperImage)
	}
}
