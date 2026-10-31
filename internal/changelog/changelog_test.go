package changelog

import (
	"reflect"
	"testing"
)

func TestHighlights(t *testing.T) {
	src := `## [Unreleased]

## [0.5.0] - 2026-11-20 [normal]

### Highlights
- A new overview.
- Audit log groups.

### Added
- Attention feed.

## [0.4.2] - 2026-10-30 [recommended]

### Highlights
- Only highlights here.
`
	sections, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		version    string
		highlights []string
		empty      bool
	}{
		{"0.5.0", []string{"A new overview.", "Audit log groups."}, false},
		{"v0.4.2", []string{"Only highlights here."}, true},
		{"Unreleased", nil, true},
	}
	for _, tt := range tests {
		s := Find(sections, tt.version)
		if s == nil {
			t.Fatalf("%s not found", tt.version)
		}
		if !reflect.DeepEqual(s.Highlights, tt.highlights) {
			t.Errorf("%s highlights = %q, want %q", tt.version, s.Highlights, tt.highlights)
		}
		if s.Empty() != tt.empty {
			t.Errorf("%s Empty() = %v, want %v", tt.version, s.Empty(), tt.empty)
		}
	}
	if m := BuildManifest(Find(sections, "0.4.2")); m.Highlights == nil || m.Changes.Added == nil {
		t.Error("manifest lists must be non-nil")
	}
}
