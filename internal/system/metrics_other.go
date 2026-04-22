//go:build !linux && !darwin

package system

import (
	"context"
	"runtime"
	"time"
)

// Collect is the fallback stub for platforms without a native metrics
// implementation (currently Windows + anything exotic). Linux uses
// metrics_linux.go, macOS uses metrics_darwin.go; only truly
// unsupported targets land here. All percentages and counts are zero;
// CPUCores is still populated so the frontend has a number to render.
func Collect() Metrics {
	m := Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "(unavailable)",
	}
	// Even on the stub platform we surface Docker's view when
	// available — it's often the most informative number the server
	// has access to without platform-specific syscalls.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return applyDockerLimits(ctx, m)
}

// StartSampler is a no-op on unsupported platforms so main.go can call
// it unconditionally.
func StartSampler(_ context.Context) {}
