package cronsched

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// every lists the instants from first, step apart, before end.
func every(first, end string, step time.Duration) []string {
	var out []string
	for t := utc(first); t.Before(utc(end)); t = t.Add(step) {
		out = append(out, t.Format(time.RFC3339))
	}
	return out
}

func TestNextAcrossDaylightSaving(t *testing.T) {
	tests := []struct {
		name, zone, spec, from string
		want                   []string
	}{
		// Fall-back 2027-10-31: 03:00 CEST becomes 02:00 CET.
		{"fall-back repeated time", "Europe/Vienna", "30 2 * * *", "2027-10-30T12:00:00Z",
			[]string{"2027-10-31T00:30:00Z", "2027-11-01T01:30:00Z"}},
		{"fall-back after repeated hour", "Europe/Vienna", "0 3 * * *", "2027-10-30T12:00:00Z",
			[]string{"2027-10-31T02:00:00Z", "2027-11-01T02:00:00Z"}},
		{"fall-back start of repeated hour", "Europe/Vienna", "0 2 * * *", "2027-10-30T12:00:00Z",
			[]string{"2027-10-31T00:00:00Z", "2027-11-01T01:00:00Z"}},
		{"fall-back every 15 min", "Europe/Vienna", "*/15 * * * *", "2027-10-30T12:00:00Z",
			every("2027-10-30T12:15:00Z", "2027-11-01T12:00:01Z", 15*time.Minute)},
		{"fall-back hourly", "Europe/Vienna", "0 * * * *", "2027-10-30T12:00:00Z",
			every("2027-10-30T13:00:00Z", "2027-11-01T12:00:01Z", time.Hour)},
		{"fall-back @daily", "Europe/Vienna", "@daily", "2027-10-30T12:00:00Z",
			[]string{"2027-10-30T22:00:00Z", "2027-10-31T23:00:00Z"}},
		{"fall-back 2025", "Europe/Vienna", "30 2 * * *", "2025-10-25T12:00:00Z",
			[]string{"2025-10-26T00:30:00Z", "2025-10-27T01:30:00Z"}},

		// Spring-forward 2027-03-28: 02:00 CET becomes 03:00 CEST.
		{"spring-forward skipped time", "Europe/Vienna", "30 2 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T01:00:00Z", "2027-03-29T00:30:00Z"}},
		{"spring-forward end of gap", "Europe/Vienna", "0 3 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T01:00:00Z", "2027-03-29T01:00:00Z"}},
		{"spring-forward start of gap", "Europe/Vienna", "0 2 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T01:00:00Z", "2027-03-29T00:00:00Z"}},
		{"spring-forward two times in gap", "Europe/Vienna", "0,30 2 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T01:00:00Z", "2027-03-29T00:00:00Z", "2027-03-29T00:30:00Z"}},
		{"spring-forward every 15 min", "Europe/Vienna", "*/15 * * * *", "2027-03-27T12:00:00Z",
			every("2027-03-27T12:15:00Z", "2027-03-29T12:00:01Z", 15*time.Minute)},
		{"spring-forward hourly", "Europe/Vienna", "0 * * * *", "2027-03-27T12:00:00Z",
			every("2027-03-27T13:00:00Z", "2027-03-29T12:00:01Z", time.Hour)},
		{"spring-forward 2025", "Europe/Vienna", "30 2 * * *", "2025-03-29T12:00:00Z",
			[]string{"2025-03-30T01:00:00Z", "2025-03-31T00:30:00Z"}},

		// Samoa skipped 2011-12-30 entirely, moving from UTC-10 to UTC+14.
		{"date-line jump", "Pacific/Apia", "30 2 * * *", "2011-12-28T22:00:00Z",
			[]string{"2011-12-29T12:30:00Z", "2011-12-30T12:30:00Z"}},

		{"UTC daily", "UTC", "30 2 * * *", "2027-10-30T12:00:00Z",
			[]string{"2027-10-31T02:30:00Z", "2027-11-01T02:30:00Z"}},
		{"UTC 03:00", "UTC", "0 3 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T03:00:00Z", "2027-03-29T03:00:00Z"}},
		{"UTC 02:00", "UTC", "0 2 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T02:00:00Z", "2027-03-29T02:00:00Z"}},
		{"UTC every 15 min", "UTC", "*/15 * * * *", "2027-10-30T12:00:00Z",
			every("2027-10-30T12:15:00Z", "2027-11-01T12:00:01Z", 15*time.Minute)},
		{"UTC hourly", "UTC", "0 * * * *", "2027-10-30T12:00:00Z",
			every("2027-10-30T13:00:00Z", "2027-11-01T12:00:01Z", time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatal(err)
			}
			sched, err := Parse(tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			from := utc(tt.from).In(loc)
			end := from.Add(48 * time.Hour)
			var got []string
			for at := sched.Next(from); !at.IsZero() && !at.After(end); at = sched.Next(at) {
				if at.Location() != loc {
					t.Fatalf("Next returned %s in %s, want %s", at, at.Location(), loc)
				}
				got = append(got, at.UTC().Format(time.RFC3339))
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d runs, want %d\ngot  %v\nwant %v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("run %d = %s, want %s\ngot  %v\nwant %v", i, got[i], tt.want[i], got, tt.want)
				}
			}
		})
	}
}

func TestNextAfterRunInRepeatedHour(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := Parse("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	ran := time.Date(2027, 10, 31, 2, 30, 0, 0, time.FixedZone("CEST", 2*3600)).In(loc)
	want := time.Date(2027, 11, 1, 2, 30, 0, 0, time.FixedZone("CET", 3600))
	tests := []struct {
		name string
		from time.Time
	}{
		{"at the run", ran},
		{"while the run is going", ran.Add(10 * time.Minute)},
		{"in the second pass", ran.Add(50 * time.Minute)},
		{"at the repeated time", ran.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sched.Next(tt.from); !got.Equal(want) {
				t.Fatalf("Next(%s) = %s, want %s", tt.from, got, want)
			}
		})
	}
}

func TestScheduleLocation(t *testing.T) {
	tests := []struct {
		name, spec, from, want string
	}{
		{"CRON_TZ prefix wins over the caller's zone", "CRON_TZ=Europe/Vienna 30 2 * * *",
			"2027-10-30T12:00:00Z", "2027-10-31T00:30:00Z"},
		{"time.Local uses the caller's zone", "30 2 * * *",
			"2027-10-30T12:00:00Z", "2027-10-31T02:30:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := Parse(tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := sched.Next(utc(tt.from)); !got.Equal(utc(tt.want)) {
				t.Fatalf("Next = %s, want %s", got.UTC(), tt.want)
			}
		})
	}
}

func TestIsInterval(t *testing.T) {
	tests := []struct {
		spec string
		want bool
	}{
		{"30 2 * * *", false},
		{"0 1-5 * * *", false},
		{"@daily", false},
		{"@weekly", false},
		{"CRON_TZ=UTC 0 3 * * *", false},
		{"*/15 * * * *", true},
		{"0 * * * *", true},
		{"0 */2 * * *", true},
		{"*/10 2 * * *", true},
		{"@hourly", true},
		{"TZ=UTC 0 * * * *", true},
	}
	for _, tt := range tests {
		if got := isInterval(tt.spec); got != tt.want {
			t.Errorf("isInterval(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, spec := range []string{"", "61 * * * *", "* * * *", "0 0 * * 7"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) = nil error", spec)
		}
	}
}

func TestMaxShiftCoversTzdata(t *testing.T) {
	zones := []string{
		"Europe/Vienna", "Europe/London", "Europe/Lisbon", "Europe/Moscow", "Europe/Dublin",
		"America/New_York", "America/Chicago", "America/Los_Angeles", "America/Sao_Paulo",
		"America/Santiago", "America/Havana", "America/St_Johns", "America/Godthab",
		"America/Asuncion", "America/Caracas", "Africa/Casablanca", "Africa/Cairo",
		"Asia/Tehran", "Asia/Jerusalem", "Asia/Gaza", "Asia/Pyongyang", "Asia/Kolkata",
		"Asia/Dhaka", "Australia/Sydney", "Australia/Lord_Howe", "Australia/Adelaide",
		"Antarctica/Troll", "Antarctica/Casey", "Pacific/Auckland", "Pacific/Chatham",
		"Pacific/Norfolk", "Pacific/Fiji", "Pacific/Apia", "Pacific/Fakaofo",
		"Pacific/Kiritimati", "Pacific/Kwajalein",
	}
	// Date-line moves drop or repeat a whole day; the gap case refuses
	// those explicitly.
	const dateLine = 24 * time.Hour
	end := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		_, prev := at.In(loc).Zone()
		for ; at.Before(end); at = at.Add(time.Hour) {
			_, off := at.In(loc).Zone()
			shift := time.Duration(off-prev) * time.Second
			prev = off
			if shift < 0 {
				shift = -shift
			}
			if shift > maxShift && shift != dateLine {
				t.Errorf("%s: change near %s moves the clock by %s, more than %s", zone, at, shift, maxShift)
			}
		}
	}
}
