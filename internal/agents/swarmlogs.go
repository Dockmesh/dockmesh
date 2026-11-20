package agents

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// StreamKindSwarmServiceLogs streams the logs of every task of one
// service (StreamOpen.Container = service id) as SwarmLogLine NDJSON;
// only opened on agents advertising CapSwarmLogs.
const StreamKindSwarmServiceLogs = "swarm.service_logs"

const (
	// MaxSwarmLogTail bounds the lines read back per task before following.
	MaxSwarmLogTail = 5000
	// MaxSwarmLogLine is the longest message kept; longer ones are cut and
	// marked Truncated.
	MaxSwarmLogLine = 16 << 10
)

// SwarmLogsReq selects what a service log stream sends. Since is a unix
// timestamp in seconds (empty = from the start), Tail a line count per
// task.
type SwarmLogsReq struct {
	Service string
	Tail    int
	Since   string
	Follow  bool
}

// Params encodes r for StreamOpen.Params.
func (r SwarmLogsReq) Params() map[string]any {
	return map[string]any{"tail": strconv.Itoa(r.Tail), "since": r.Since, "follow": r.Follow}
}

// SwarmLogsReqFrom decodes StreamOpen params, clamping what is out of
// bounds.
func SwarmLogsReqFrom(service string, p map[string]any) SwarmLogsReq {
	r := SwarmLogsReq{Service: service, Tail: 100, Follow: true}
	if s, ok := p["tail"].(string); ok {
		if n, err := strconv.Atoi(s); err == nil {
			r.Tail = min(max(n, 0), MaxSwarmLogTail)
		}
	}
	if s, ok := p["since"].(string); ok && ValidUnixSeconds(s) {
		r.Since = s
	}
	if v, ok := p["follow"].(bool); ok {
		r.Follow = v
	}
	return r
}

// ValidUnixSeconds reports whether s is a non-negative unix time in
// seconds, optionally with a fraction, as dockerd's since accepts it.
func ValidUnixSeconds(s string) bool {
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" || len(whole) > 12 || (hasFrac && (frac == "" || len(frac) > 9)) {
		return false
	}
	for _, c := range whole + frac {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SwarmLogLine is one log message of a service task. Only the task and
// node identity is taken from the log metadata; attributes a log driver
// adds from labels or env are dropped.
type SwarmLogLine struct {
	Time      time.Time `json:"ts,omitzero"`
	Task      string    `json:"task"` // "<service>.<slot>" or "<service>.<node id>" for global services
	TaskID    string    `json:"task_id"`
	Node      string    `json:"node,omitempty"` // node host name
	NodeID    string    `json:"node_id"`
	Stream    string    `json:"stream"` // stdout | stderr
	Message   string    `json:"msg"`
	Truncated bool      `json:"truncated,omitempty"`
}

// SanitizeSwarmLogLine bounds every field of a line that came from an
// agent; ok is false when it doesn't name a task.
func SanitizeSwarmLogLine(l SwarmLogLine) (SwarmLogLine, bool) {
	l.TaskID = token(l.TaskID, 64, "")
	l.NodeID = token(l.NodeID, 64, "")
	if l.TaskID == "" {
		return SwarmLogLine{}, false
	}
	l.Task = text(l.Task, 200)
	l.Node = hostname(l.Node)
	if l.Stream != "stderr" {
		l.Stream = "stdout"
	}
	l.Message, l.Truncated = ClipLogMessage(l.Message, l.Truncated)
	return l, true
}

// ClipLogMessage makes msg valid UTF-8 and cuts it to MaxSwarmLogLine
// bytes on a rune boundary.
func ClipLogMessage(msg string, truncated bool) (string, bool) {
	if !utf8.ValidString(msg) {
		msg = strings.ToValidUTF8(msg, "�")
	}
	if len(msg) <= MaxSwarmLogLine {
		return msg, truncated
	}
	cut := MaxSwarmLogLine
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut], true
}
