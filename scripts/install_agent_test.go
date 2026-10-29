package scripts

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallAgentLogState(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	b, err := os.ReadFile("install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "agent_log_state() {")
	end := strings.Index(s, "# end agent_log_state")
	if start < 0 || end < start {
		t.Fatal("agent_log_state not found in install-agent.sh")
	}
	fn := s[start:end]

	tests := []struct {
		name, logs, want string
	}{
		{"empty", "", "waiting"},
		{"enrolling", `{"level":"INFO","msg":"enrolling","url":"https://s:8443"}`, "waiting"},
		{"enrolled and connected", `{"msg":"enrolled successfully"}` + "\n" + `{"msg":"connected","agent_id":"a"}`, "connected"},
		{"enrollment rejected", `{"msg":"enrolling"}` + "\n" + `{"level":"ERROR","msg":"enrollment failed","err":"token expired"}`, "failed"},
		{"tls error after enroll", `{"msg":"enrolled successfully"}` + "\n" + `{"msg":"disconnected, reconnecting","err":"x509"}`, "waiting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+fn+"\nagent_log_state \"$1\"", "t", tt.logs)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
