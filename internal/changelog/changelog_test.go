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

func TestActionBulletRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		bullet string
	}{
		{"everyone has no label", Action{ID: "re-login", Audience: "all", Text: "Sign in again."}, "Sign in again. <!-- re-login -->"},
		{"admins are labelled", Action{ID: "sftp-host-key", Audience: "admins", Text: "Re-save each target."}, "**Admins:** Re-save each target. <!-- sftp-host-key -->"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActionBullet(tt.action); got != tt.bullet {
				t.Fatalf("ActionBullet = %q, want %q", got, tt.bullet)
			}
			sections, err := Parse("## [Unreleased]\n### Upgrade notes\n- " + tt.bullet + "\n")
			if err != nil {
				t.Fatal(err)
			}
			if got := sections[0].Actions; len(got) != 1 || got[0] != tt.action {
				t.Fatalf("parsed %+v, want %+v", got, tt.action)
			}
		})
	}
}

func TestParseOldActionForm(t *testing.T) {
	sections, err := Parse("## [Unreleased]\n### Upgrade notes\n- [re-login, all] Sign in again.\n- [sftp, admins] Re-save.\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []Action{{ID: "re-login", Audience: "all", Text: "Sign in again."}, {ID: "sftp", Audience: "admins", Text: "Re-save."}}
	if got := sections[0].Actions; !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed %+v, want %+v", got, want)
	}
}
