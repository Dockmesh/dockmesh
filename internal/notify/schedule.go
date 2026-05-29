package notify

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// isMutedNow mirrors internal/alerts/schedule.go::IsMutedNow. The
// alerts package can't be imported here (cycle: alerts imports notify),
// so we duplicate the ~50 lines of weekly-schedule logic. Keep the two
// in sync — if you add a new schedule "type", update both.
//
// The schedule JSON shape is:
//
//	{"type":"weekly","ranges":[{"days":[1,2,3,4,5],"from":"22:00","to":"06:00"}]}
//
// Empty string, malformed JSON, or unknown type return false (not muted)
// so "no schedule set" stays the safe default.
func isMutedNow(scheduleJSON string, t time.Time) bool {
	s := strings.TrimSpace(scheduleJSON)
	if s == "" {
		return false
	}
	var sched struct {
		Type   string `json:"type"`
		Ranges []struct {
			Days []int  `json:"days"`
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"ranges"`
	}
	if err := json.Unmarshal([]byte(s), &sched); err != nil {
		return false
	}
	if sched.Type != "weekly" || len(sched.Ranges) == 0 {
		return false
	}
	weekday := int(t.Weekday())
	prevWeekday := (weekday + 6) % 7
	mins := t.Hour()*60 + t.Minute()

	for _, r := range sched.Ranges {
		from, fromOK := parseHHMM(r.From)
		to, toOK := parseHHMM(r.To)
		if !fromOK || !toOK || from == to {
			continue
		}
		if from < to {
			if !dayMatches(r.Days, weekday) {
				continue
			}
			if mins >= from && mins < to {
				return true
			}
			continue
		}
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
		return true
	}
	for _, d := range days {
		if d == weekday {
			return true
		}
	}
	return false
}
