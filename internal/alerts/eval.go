package alerts

import "time"

// DefaultSampleInterval matches the metrics collector's polling period.
// It doubles as the sampling tolerance of the duration gate and as the
// "clear for one interval" requirement before a firing alert resolves.
const DefaultSampleInterval = 30 * time.Second

// staleFactor × interval without any sample means the container is gone
// (stopped, removed or no longer matched) and a firing alert resolves.
const staleFactor = 3

// metricSample is one metrics_raw row reduced to the evaluated value.
type metricSample struct {
	TS    int64
	Value float64
}

func breaches(op string, v, threshold float64) bool {
	switch op {
	case "gt":
		return v > threshold
	case "lt":
		return v < threshold
	}
	return false
}

// decide returns whether the alert should be firing after looking at
// samples (ascending by TS) at time now.
//
// Fire: every sample of the trailing breach run satisfies the condition,
// consecutive samples are at most two intervals apart (one missed
// collection is tolerated), and the run reaches back to now-duration
// within one interval. The tolerance is needed because the first sample
// inside the window can land up to one interval after its start.
//
// Resolve: the trailing run of non-breaching samples spans at least one
// interval, or the series went stale. A single good sample between bad
// ones therefore keeps the alert firing instead of flapping.
func decide(samples []metricSample, op string, threshold float64, duration time.Duration, wasFiring bool, now time.Time, interval time.Duration) bool {
	if len(samples) == 0 {
		return false
	}
	nowS := now.Unix()
	tol := int64(interval / time.Second)
	if tol < 1 {
		tol = 1
	}
	last := samples[len(samples)-1]
	if nowS-last.TS > staleFactor*tol {
		return false
	}

	matching := breaches(op, last.Value, threshold)
	runStart := last.TS
	for i := len(samples) - 2; i >= 0; i-- {
		s := samples[i]
		if breaches(op, s.Value, threshold) != matching || runStart-s.TS > 2*tol {
			break
		}
		runStart = s.TS
	}
	held := nowS - runStart

	if wasFiring {
		if matching {
			return true
		}
		return held < tol
	}
	if !matching {
		return false
	}
	return held >= int64(duration/time.Second)-tol
}
