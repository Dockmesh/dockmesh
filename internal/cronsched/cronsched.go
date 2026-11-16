// Package cronsched parses 5-field cron expressions into schedules that
// follow classic cron (Vixie/cronie) across daylight-saving changes.
//
// A fixed-time schedule (no `*` in the minute or hour field, e.g.
// `30 2 * * *` or `@daily`) runs once on a night whose clock falls back,
// at the first occurrence of the repeated time, and a time the clock
// skips runs when the skipped hour ends. An interval schedule (`*` in the
// minute or hour field, e.g. `*/15 * * * *`, `0 * * * *`, `@hourly`)
// follows the clock: it runs in both passes of a repeated hour and not at
// times that are skipped.
package cronsched

import (
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// maxShift bounds how far a daylight-saving change moves the clock.
const maxShift = 3 * time.Hour

// Parse parses a standard cron expression as cron.ParseStandard does.
func Parse(spec string) (cron.Schedule, error) {
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, err
	}
	ss, ok := sched.(*cron.SpecSchedule)
	if !ok {
		return sched, nil
	}
	wall := *ss
	wall.Location = time.UTC
	return &schedule{wall: wall, loc: ss.Location, interval: isInterval(spec)}, nil
}

// schedule evaluates the cron fields on a wall clock without
// daylight-saving changes (wall, in UTC) and maps each matching wall
// time to the instants it has in loc.
type schedule struct {
	wall     cron.SpecSchedule
	loc      *time.Location
	interval bool
}

func (s *schedule) Next(t time.Time) time.Time {
	loc := s.loc
	if loc == time.Local {
		loc = t.Location()
	}
	// Starting before t's own wall time finds the second pass of a
	// repeated hour, whose wall times lie before t's.
	w := wallClock(t.In(loc)).Add(-maxShift)
	limit := w.Year() + 5
	var best time.Time
	for {
		w = s.wall.Next(w)
		if w.IsZero() || w.Year() > limit {
			break
		}
		if !best.IsZero() && w.Sub(wallClock(best.In(loc))) > maxShift {
			break
		}
		for _, at := range s.instants(w, loc) {
			if at.After(t) && (best.IsZero() || at.Before(best)) {
				best = at
			}
		}
	}
	if best.IsZero() {
		return best
	}
	return best.In(t.Location())
}

// instants returns when wall time w (fields in UTC) happens in loc,
// earliest first.
func (s *schedule) instants(w time.Time, loc *time.Location) []time.Time {
	_, before := w.Add(-24 * time.Hour).In(loc).Zone()
	_, after := w.Add(24 * time.Hour).In(loc).Zone()
	offsets := []int{before}
	if after != before {
		offsets = append(offsets, after)
	}
	var out []time.Time
	for _, off := range offsets {
		at := w.Add(-time.Duration(off) * time.Second)
		if _, o := at.In(loc).Zone(); o == off {
			out = append(out, at)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	if s.interval {
		return out
	}
	switch len(out) {
	case 0:
		// w lies in a skipped hour: w read with the old offset is already
		// past the change, whose zone period starts when the gap ends.
		start, _ := w.Add(-time.Duration(before) * time.Second).In(loc).ZoneBounds()
		if start.IsZero() {
			return nil
		}
		return []time.Time{start}
	case 2:
		return out[:1]
	}
	return out
}

func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// isInterval reports whether the minute or hour field starts with a
// wildcard, which is how cron tells interval jobs from fixed-time ones.
func isInterval(spec string) bool {
	f := strings.Fields(spec)
	if len(f) > 0 && (strings.HasPrefix(f[0], "TZ=") || strings.HasPrefix(f[0], "CRON_TZ=")) {
		f = f[1:]
	}
	if len(f) == 0 {
		return false
	}
	if f[0] == "@hourly" {
		return true
	}
	wild := func(field string) bool { return strings.HasPrefix(field, "*") || strings.HasPrefix(field, "?") }
	return len(f) >= 2 && (wild(f[0]) || wild(f[1]))
}
