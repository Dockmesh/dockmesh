package alerts

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// MuteSchedule is the JSON shape stored in the mute_schedule columns
// on alert_rules and notification_channels. Only the "weekly" type is
// honoured by IsMutedNow today; other shapes are reserved.
type MuteSchedule struct {
	Type   string             `json:"type"`
	Ranges []MuteScheduleRange `json:"ranges"`
}

// MuteScheduleRange is one quiet-hour window. Days are time.Weekday
// integers (0=Sunday … 6=Saturday). From/To are "HH:MM" 24-hour
// timestamps. To < From means the window wraps past midnight (e.g.
// from=22:00 to=06:00 mutes from 22:00 today through 06:00 the next
// morning) — useful for night-shift quiet hours.
type MuteScheduleRange struct {
	Days []int  `json:"days"`
	From string `json:"from"`
	To   string `json:"to"`
}

// IsMutedNow reports whether the JSON-encoded schedule blob matches
// time t. An empty string, malformed JSON, an unknown type, and a
// schedule with no ranges all evaluate to false (not muted). This makes
// "no schedule set" the safe default — alerts keep flowing.
func IsMutedNow(scheduleJSON string, t time.Time) bool {
	s := strings.TrimSpace(scheduleJSON)
	if s == "" {
		return false
	}
	var sched MuteSchedule
	if err := json.Unmarshal([]byte(s), &sched); err != nil {
		return false
	}
	if sched.Type != "weekly" || len(sched.Ranges) == 0 {
		return false
	}
	// Resolve "now" into local components so the comparison is in the
	// server's TZ — same TZ the operator uses to pick HH:MM in the UI.
	weekday := int(t.Weekday())
	mins := t.Hour()*60 + t.Minute()

	prevWeekday := (weekday + 6) % 7

	for _, r := range sched.Ranges {
		from, fromOK := parseHHMM(r.From)
		to, toOK := parseHHMM(r.To)
		if !fromOK || !toOK {
			continue
		}
		if from == to {
			// Empty / degenerate range — treat as not muted.
			continue
		}
		if from < to {
			// Same-day window.
			if !dayMatches(r.Days, weekday) {
				continue
			}
			if mins >= from && mins < to {
				return true
			}
			continue
		}
		// Cross-midnight window. The range fires when:
		//   today  is in `days` AND now >= from        (evening half)
		//   yesterday is in `days` AND now < to        (morning half)
		if dayMatches(r.Days, weekday) && mins >= from {
			return true
		}
		if dayMatches(r.Days, prevWeekday) && mins < to {
			return true
		}
	}
	return false
}

func parseHHMM(s string) (int, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 24 {
		return 0, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	if h == 24 && m == 0 {
		return 24 * 60, true
	}
	if h == 24 {
		return 0, false
	}
	return h*60 + m, true
}

func dayMatches(days []int, weekday int) bool {
	if len(days) == 0 {
		// Empty days list — interpret as "every day" so a user-friendly
		// "always mute between 22-06" can omit the days array.
		return true
	}
	for _, d := range days {
		if d == weekday {
			return true
		}
	}
	return false
}
