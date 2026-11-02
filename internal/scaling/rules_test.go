package scaling

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/metrics"
)

func TestCPUUnit(t *testing.T) {
	// 150 per core on a 4-core host is 37.5 % of the host.
	s := metrics.Sample{CPUPercentCore: 150, CPUPercent: 37.5}
	tests := []struct {
		unit    string
		want    float64
		invalid bool
	}{
		{unit: "", want: 150},
		{unit: CPUUnitCore, want: 150},
		{unit: CPUUnitHost, want: 37.5},
		{unit: "percent", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.unit, func(t *testing.T) {
			r := Rule{Service: "web", MaxReplicas: 2, CPUUnit: tt.unit}
			err := (&ScalingConfig{Rules: []Rule{r}}).Validate()
			if tt.invalid {
				if err == nil {
					t.Fatal("invalid unit accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cpuValue(s, r.Unit()); got != tt.want {
				t.Fatalf("cpu value = %v, want %v", got, tt.want)
			}
		})
	}
}
