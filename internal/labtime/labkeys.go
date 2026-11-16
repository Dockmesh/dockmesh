//go:build labkeys

package labtime

import (
	"log/slog"
	"os"
	"time"
)

func init() {
	d, _ := time.ParseDuration(os.Getenv("DOCKMESH_LAB_DAY"))
	if d > 0 && d < 24*time.Hour {
		slog.Warn("lab build: day-scale periods are shortened", "day", d)
	}
	day = func() time.Duration { return d }
}
