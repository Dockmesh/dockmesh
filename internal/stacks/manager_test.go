package stacks

import (
	"errors"
	"testing"
)

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"ok lowercase", "nextcloud", nil},
		{"ok with digits", "app1", nil},
		{"ok with hyphen", "my-stack", nil},
		{"too short", "a", ErrInvalidName},
		{"uppercase", "NextCloud", ErrInvalidName},
		{"leading hyphen", "-stack", ErrInvalidName},
		{"trailing hyphen", "stack-", ErrInvalidName},
		{"underscore", "my_stack", ErrInvalidName},
		{"dot", "a.b", ErrInvalidName},
		{"slash traversal", "../etc", ErrInvalidName},
		{"reserved dockmesh", "dockmesh", ErrReserved},
		{"reserved admin", "admin", ErrReserved},
		{"empty", "", ErrInvalidName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.in)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}
