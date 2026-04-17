package main

import "testing"

func TestSlugifyStackName(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		errOK  bool // true = we expect an error, don't check want
	}{
		// Obvious happy paths.
		{in: "nginx", want: "nginx"},
		{in: "my-app", want: "my-app"},
		{in: "App01", want: "app01"},

		// Portainer-ish and Dockge-ish names.
		{in: "My Stack", want: "my-stack"},
		{in: "my_stack_01", want: "my-stack-01"},
		{in: "nginx (copy)", want: "nginx-copy"},
		{in: "  leading space", want: "leading-space"},

		// Collapse consecutive separators.
		{in: "a   b", want: "a-b"},
		{in: "a___b", want: "a-b"},
		{in: "a---b", want: "a-b"},

		// Reserved names produce an error.
		{in: "admin", errOK: true},
		{in: "system", errOK: true},
		{in: "dockmesh", errOK: true},

		// Empty / whitespace-only / punctuation-only → error.
		{in: "", errOK: true},
		{in: "   ", errOK: true},
		{in: "___", errOK: true},
		{in: "!!!", errOK: true},

		// Single char is too short (ValidateName requires 2..63).
		{in: "a", errOK: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := slugifyStackName(c.in)
			if c.errOK {
				if err == nil {
					t.Fatalf("expected error for %q, got slug %q", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("slugify(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
