// Package labtime shortens day-scale periods in lab builds so the release
// gate can watch them pass inside a test run. Release builds never set
// day, so Scale returns its argument unchanged.
package labtime

import "time"

// day is set only by builds with the labkeys tag.
var day func() time.Duration

// minTick keeps scaled ticker intervals from spinning.
const minTick = time.Second

// Scale maps a period measured against a 24-hour day onto the lab day.
func Scale(d time.Duration) time.Duration {
	if day == nil {
		return d
	}
	ld := day()
	if ld <= 0 || ld >= 24*time.Hour {
		return d
	}
	return time.Duration(float64(d) * float64(ld) / float64(24*time.Hour))
}

// Tick is Scale for ticker intervals, floored at one second (or at d, if d
// is shorter).
func Tick(d time.Duration) time.Duration {
	return max(Scale(d), min(d, minTick))
}
