//go:build !linux

package system

import "runtime"

// Collect is a dev-only stub for non-Linux builds (macOS/Windows). All
// percentages and counts are zero; CPUCores is still populated so the
// frontend has a number to render.
func Collect() Metrics {
	return Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "(unavailable)",
	}
}
