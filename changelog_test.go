package dockmesh

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/changelog"
)

func TestEmbeddedChangelogParses(t *testing.T) {
	sections, err := changelog.Parse(Changelog)
	if err != nil {
		t.Fatal(err)
	}
	if changelog.Find(sections, changelog.Unreleased) == nil {
		t.Error("embedded changelog has no [Unreleased] section")
	}
}
