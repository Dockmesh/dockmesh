package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
)

func TestDeployErrorStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: /dev/zero is not a regular file", compose.ErrComposeFileRef), http.StatusUnprocessableEntity},
		{errors.New("agent: compose file reference refused: ../x.yaml is outside the stack folder"), http.StatusUnprocessableEntity},
		{errors.New("port is already allocated"), http.StatusUnprocessableEntity},
		{errors.New("docker daemon gone"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := deployErrorStatus(tc.err); got != tc.want {
			t.Errorf("deployErrorStatus(%q) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
