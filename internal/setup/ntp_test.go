package setup

import "testing"

func TestNTPSynced(t *testing.T) {
	tests := []struct {
		out  string
		want bool
	}{
		{"yes\n", true},
		{"YES", true},
		{"no\n", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ntpSynced(tt.out); got != tt.want {
			t.Errorf("ntpSynced(%q) = %v, want %v", tt.out, got, tt.want)
		}
	}
}
