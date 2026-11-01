package docker

import (
	"math"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func frame(cpu, preCPU, sys, preSys uint64, online uint32, usage uint64, stats map[string]uint64) *container.StatsResponse {
	var s container.StatsResponse
	s.CPUStats.CPUUsage.TotalUsage = cpu
	s.PreCPUStats.CPUUsage.TotalUsage = preCPU
	s.CPUStats.SystemUsage = sys
	s.PreCPUStats.SystemUsage = preSys
	s.CPUStats.OnlineCPUs = online
	s.MemoryStats.Usage = usage
	s.MemoryStats.Limit = 1000
	s.MemoryStats.Stats = stats
	return &s
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name            string
		in              *container.StatsResponse
		cpuHost, cpuCor float64
		memUsed         uint64
	}{
		{
			name:    "cgroup v2 subtracts inactive_file",
			in:      frame(300, 100, 2000, 1000, 4, 500, map[string]uint64{"inactive_file": 200, "active_file": 50}),
			cpuHost: 20, cpuCor: 80, memUsed: 300,
		},
		{
			name:    "cgroup v1 subtracts total_inactive_file",
			in:      frame(300, 100, 2000, 1000, 4, 500, map[string]uint64{"total_inactive_file": 120, "cache": 400, "inactive_file": 100}),
			cpuHost: 20, cpuCor: 80, memUsed: 380,
		},
		{
			name:    "cache larger than usage keeps usage",
			in:      frame(0, 0, 0, 0, 2, 500, map[string]uint64{"inactive_file": 900}),
			memUsed: 500,
		},
		{
			name:    "counter reset after restart reads 0 CPU",
			in:      frame(50, 100, 2000, 1000, 2, 10, nil),
			memUsed: 10,
		},
		{
			name: "online CPUs missing falls back to per-CPU list",
			in: func() *container.StatsResponse {
				f := frame(200, 100, 2000, 1000, 0, 10, nil)
				f.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 1}
				return f
			}(),
			cpuHost: 10, cpuCor: 20, memUsed: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.in)
			if math.Abs(got.CPUPercent-tt.cpuHost) > 1e-9 || math.Abs(got.CPUPercentCore-tt.cpuCor) > 1e-9 {
				t.Errorf("cpu = %v host / %v core, want %v / %v", got.CPUPercent, got.CPUPercentCore, tt.cpuHost, tt.cpuCor)
			}
			if got.MemUsed != tt.memUsed {
				t.Errorf("mem used = %d, want %d", got.MemUsed, tt.memUsed)
			}
		})
	}
}

func TestNoMemLimit(t *testing.T) {
	const gib = 1 << 30
	tests := []struct {
		limit, host uint64
		want        bool
	}{
		{0, 4 * gib, true},
		{4 * gib, 4 * gib, true},
		{4*gib - 10, 4 * gib, true},
		{2 * gib, 4 * gib, false},
		{2 * gib, 0, false},
	}
	for _, tt := range tests {
		if got := NoMemLimit(tt.limit, tt.host); got != tt.want {
			t.Errorf("NoMemLimit(%d, %d) = %v, want %v", tt.limit, tt.host, got, tt.want)
		}
	}
}
