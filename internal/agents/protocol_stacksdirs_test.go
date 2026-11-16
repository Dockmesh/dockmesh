package agents

import (
	"strings"
	"testing"
)

func TestReportedStacksDirs(t *testing.T) {
	long := "/" + strings.Repeat("a", 5000)
	tests := []struct {
		name string
		in   []StacksDirInfo
		want int
	}{
		{"none", nil, 0},
		{"two", []StacksDirInfo{{Path: "/srv/s", Current: true, Stacks: 2}, {Path: "/old", Stacks: 1}}, 2},
		{"capped", []StacksDirInfo{{Path: "/a"}, {Path: "/b"}, {Path: "/c"}, {Path: "/d"}, {Path: "/e"}}, maxStacksDirs},
	}
	for _, tt := range tests {
		got := HelloPayload{StacksDirs: tt.in}.ReportedStacksDirs()
		if len(got) != tt.want {
			t.Errorf("%s: %d entries, want %d", tt.name, len(got), tt.want)
		}
	}
	got := HelloPayload{StacksDirs: []StacksDirInfo{{Path: long}}}.ReportedStacksDirs()
	if len(got[0].Path) != 4096 {
		t.Errorf("path kept %d bytes", len(got[0].Path))
	}
}
