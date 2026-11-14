package migration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
)

type hookHost struct {
	host.Host
	exitCode int
	output   string
	execErr  error
	ran      []string
}

func (h *hookHost) StackStatus(context.Context, string) ([]compose.StatusEntry, error) {
	return []compose.StatusEntry{{Service: "db", State: "running", ContainerID: "c1"}}, nil
}

func (h *hookHost) ContainerExec(_ context.Context, id string, cmd []string) ([]byte, int, error) {
	h.ran = append(h.ran, id+": "+strings.Join(cmd, " "))
	return []byte(h.output), h.exitCode, h.execErr
}

func TestExecuteHookChecksExitCode(t *testing.T) {
	cases := []struct {
		name    string
		host    *hookHost
		wantErr string
	}{
		{"exit 0", &hookHost{output: "dumped"}, ""},
		{"non-zero exit fails with the output", &hookHost{exitCode: 3, output: "pg_dump: connection refused\n"}, "exited with code 3: pg_dump: connection refused"},
		{"no output", &hookHost{exitCode: 1}, "exited with code 1: no output"},
		{"exec error", &hookHost{execErr: errors.New("agent gone")}, "exec: agent gone"},
	}
	s := &Service{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.executeHook(context.Background(), c.host, "web", "pre_dump", "pg_dump -f /dump")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
			}
			if len(c.host.ran) != 1 || c.host.ran[0] != "c1: sh -c pg_dump -f /dump" {
				t.Fatalf("ran %q", c.host.ran)
			}
		})
	}
}

func TestOutputTailKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("x", 600) + "the error"
	got := outputTail([]byte(long))
	if !strings.HasSuffix(got, "the error") || len(got) > 510 {
		t.Fatalf("outputTail = %q (%d bytes)", got, len(got))
	}
}
