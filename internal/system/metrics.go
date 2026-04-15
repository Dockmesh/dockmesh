// Package system reads host-level metrics (CPU / memory / disk) for the
// dashboard's "System health" panel. Linux is the production target;
// non-Linux builds (dev on macOS/Windows) return zeros via metrics_other.go
// so the frontend doesn't crash.
package system

// Metrics captures a point-in-time snapshot of host load. All percentages
// are 0..100, all byte counts are raw bytes so the frontend can format
// them however it wants (GiB, GB, etc).
type Metrics struct {
	CPUPercent  float64 `json:"cpu_percent"`
	CPUCores    int     `json:"cpu_cores"`
	CPUUsed     float64 `json:"cpu_used_cores"` // fractional cores in use
	MemPercent  float64 `json:"mem_percent"`
	MemTotal    uint64  `json:"mem_total"`
	MemUsed     uint64  `json:"mem_used"`
	DiskPercent float64 `json:"disk_percent"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskPath    string  `json:"disk_path"`
	Uptime      int64   `json:"uptime_seconds"`
}
