package agents

import (
	"strings"
	"testing"
)

func TestSwarmLogsReqRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   SwarmLogsReq
	}{
		{"defaults", nil, SwarmLogsReq{Service: "s1", Tail: 100, Follow: true}},
		{"set", SwarmLogsReq{Tail: 7, Since: "1700000000.5", Follow: false}.Params(), SwarmLogsReq{Service: "s1", Tail: 7, Since: "1700000000.5"}},
		{"clamped tail", map[string]any{"tail": "999999"}, SwarmLogsReq{Service: "s1", Tail: MaxSwarmLogTail, Follow: true}},
		{"negative tail", map[string]any{"tail": "-3"}, SwarmLogsReq{Service: "s1", Tail: 0, Follow: true}},
		{"bad since", map[string]any{"since": "10m; rm"}, SwarmLogsReq{Service: "s1", Tail: 100, Follow: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SwarmLogsReqFrom("s1", tt.params); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidUnixSeconds(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "1700000000": true, "1700000000.123456789": true,
		"": false, "-1": false, "1.": false, ".5": false, "1e9": false, "10m": false,
		"1700000000.1234567890": false, "1234567890123": false,
	} {
		if got := ValidUnixSeconds(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}

func TestSanitizeSwarmLogLine(t *testing.T) {
	if _, ok := SanitizeSwarmLogLine(SwarmLogLine{TaskID: "Bad ID"}); ok {
		t.Fatal("a line with an invalid task id was kept")
	}
	l, ok := SanitizeSwarmLogLine(SwarmLogLine{
		TaskID: "t1", NodeID: "n1", Task: "Web.1\x1b[2J", Node: "m1 evil", Stream: "other",
		Message: strings.Repeat("é", MaxSwarmLogLine) + "\xff",
	})
	if !ok {
		t.Fatal("line dropped")
	}
	if l.Task != "Web.1[2J" || l.Node != "" || l.Stream != "stdout" || !l.Truncated || len(l.Message) > MaxSwarmLogLine {
		t.Fatalf("got task %q node %q stream %q truncated %v len %d", l.Task, l.Node, l.Stream, l.Truncated, len(l.Message))
	}
	if !strings.HasSuffix(l.Message, "é") {
		t.Fatal("message cut inside a rune")
	}
}
