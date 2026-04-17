package rbac

import "testing"

func TestScopeMatchesHost(t *testing.T) {
	cases := []struct {
		name      string
		userScope []string
		hostTags  []string
		want      bool
	}{
		{
			name:      "empty scope = all hosts",
			userScope: nil,
			hostTags:  []string{"prod"},
			want:      true,
		},
		{
			name:      "nil slice treated as empty",
			userScope: []string{},
			hostTags:  []string{"prod"},
			want:      true,
		},
		{
			name:      "single tag match",
			userScope: []string{"prod"},
			hostTags:  []string{"prod", "eu"},
			want:      true,
		},
		{
			name:      "tag mismatch",
			userScope: []string{"prod"},
			hostTags:  []string{"staging"},
			want:      false,
		},
		{
			name:      "any-of: user has [a,b], host has [b,c] → match",
			userScope: []string{"team-a", "team-b"},
			hostTags:  []string{"team-b", "team-c"},
			want:      true,
		},
		{
			name:      "any-of: no overlap → fail",
			userScope: []string{"team-a", "team-b"},
			hostTags:  []string{"team-c"},
			want:      false,
		},
		{
			name:      "host has no tags + user has scope → fail",
			userScope: []string{"prod"},
			hostTags:  []string{},
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeMatchesHost(tc.userScope, tc.hostTags)
			if got != tc.want {
				t.Errorf("ScopeMatchesHost(%v, %v) = %v, want %v",
					tc.userScope, tc.hostTags, got, tc.want)
			}
		})
	}
}
