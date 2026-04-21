//go:build !linux

package system

import (
	"context"
	"runtime"
)

// Collect is a dev-only stub for non-Linux builds (macOS/Windows). All
// percentages and counts are zero; CPUCores is still populated so the
// frontend has a number to render.
func Collect() Metrics {
	return Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "(unavailable)",
	}
}

// StartSampler is a no-op on non-Linux builds so main.go can call it
// unconditionally.
func StartSampler(_ context.Context) {}
