package host

import (
	"context"
	"strings"
	"testing"
)

// Agents older than the server don't refuse tracking labels, so the
// server checks a remote deploy before sending it.
func TestRemoteDeployChecksReservedLabels(t *testing.T) {
	tests := []struct {
		name, compose string
		wantErr       bool
	}{
		{"own labels", "services:\n  web:\n    image: nginx\n    labels:\n      team: web\n", false},
		{"another project", "services:\n  web:\n    image: nginx\n    labels:\n      com.docker.compose.project: billing\n", true},
		{"interpolated project", "services:\n  web:\n    image: nginx\n    labels:\n      com.docker.compose.project: ${OTHER}\n", true},
		{"left to the agent when unparsable", "services: [\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkReservedLabels(context.Background(), "shop", tt.compose, "OTHER=billing\n")
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "is reserved for Dockmesh") {
				t.Errorf("error %q does not name the reservation", err)
			}
		})
	}
}
