//go:build !linux && !darwin

package system

import (
	"context"
	"runtime"
)

// Collect is the fallback stub for platforms without a native metrics
// implementation (currently Windows + anything exotic). Linux uses
// metrics_linux.go, macOS uses metrics_darwin.go; only truly
// unsupported targets land here. All percentages and counts are zero;
// CPUCores is still populated so the frontend has a number to render.
func Collect() Metrics {
	return Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "(unavailable)",
	}
}

// StartSampler is a no-op on unsupported platforms so main.go can call
// it unconditionally.
func StartSampler(_ context.Context) {}
