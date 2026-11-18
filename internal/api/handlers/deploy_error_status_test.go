package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
)

func TestDeployErrorStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: /dev/zero is not a regular file", compose.ErrComposeFileRef), http.StatusUnprocessableEntity},
		{errors.New("agent: compose file reference refused: ../x.yaml is outside the stack folder"), http.StatusUnprocessableEntity},
		{fmt.Errorf("service \"web\": env_file \"/etc/hostname\" is absolute: %w", compose.ErrEnvFileNeedAgent), http.StatusUnprocessableEntity},
		{fmt.Errorf("key \"LABEL_FILE\": write it as \"label_file\": %w", compose.ErrLabelsNeedAgent), http.StatusUnprocessableEntity},
		{errors.New("port is already allocated"), http.StatusUnprocessableEntity},
		{errors.New("docker daemon gone"), http.StatusInternalServerError},
		{fmt.Errorf("deploy: %w", &agents.WaitingForSetupError{Name: "edge-1"}), http.StatusConflict},
	}
	for _, tc := range cases {
		if got := deployErrorStatus(tc.err); got != tc.want {
			t.Errorf("deployErrorStatus(%q) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestFriendlyDeployErrorKeepsRefusal(t *testing.T) {
	err := errors.New("agent: compose file reference refused: /stacks/x/compose.yaml: path ~/x.yaml starts with ~")
	if got := friendlyDeployError(err); got != err.Error() {
		t.Fatalf("friendlyDeployError = %q", got)
	}
}
