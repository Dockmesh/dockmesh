package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
)

// MigrationHooks is the migration-specific section from .dockmesh.meta.json.
type MigrationHooks struct {
	PreDump     string `json:"pre_dump,omitempty"`      // shell command run via docker exec before stop
	DumpPath    string `json:"dump_path,omitempty"`      // path inside container to transfer instead of volumes
	PostRestore string `json:"post_restore,omitempty"`   // shell command run via docker exec after start on target
}

// loadHooks reads migration hooks from the stack's meta file.
func (s *Service) loadHooks(stackName string) (*MigrationHooks, error) {
	dir, err := s.stacks.Dir(stackName)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".dockmesh.meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var meta struct {
		Migration *MigrationHooks `json:"migration"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return meta.Migration, nil
}

// hookTimeout bounds one hook, like the agent's request timeout does for
// remote hosts. Docker can't kill an exec, so a hook that times out keeps
// running in its container.
var hookTimeout = 30 * time.Minute

// executeHook runs a shell command via docker exec on the first running
// container of the stack on the given host. This is how pre_dump and
// post_restore hooks are executed. A non-zero exit fails the phase.
func (s *Service) executeHook(ctx context.Context, h host.Host, stackName, hookName, cmd string) error {
	if cmd == "" {
		return nil
	}
	slog.Info("migration hook executing",
		"stack", stackName, "hook", hookName, "cmd", cmd)

	status, err := h.StackStatus(ctx, stackName)
	if err != nil {
		return fmt.Errorf("hook %s: stack status: %w", hookName, err)
	}
	var containerID string
	for _, entry := range status {
		if entry.State == "running" {
			containerID = entry.ContainerID
			break
		}
	}
	if containerID == "" {
		return fmt.Errorf("hook %s: no running container found for stack %s", hookName, stackName)
	}

	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	output, exitCode, err := h.ContainerExec(hookCtx, containerID, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("hook %s: exec: %w", hookName, err)
	}
	if exitCode != 0 {
		// The output stays in the server log: the migration error is shown
		// to stack viewers on both hosts and may not see the container's
		// environment.
		slog.Warn("migration hook failed",
			"stack", stackName, "hook", hookName,
			"exit_code", exitCode, "output", outputTail(output))
		return fmt.Errorf("hook %s exited with code %d (output in the server log)", hookName, exitCode)
	}
	slog.Info("migration hook completed",
		"stack", stackName, "hook", hookName, "output_bytes", len(output))
	return nil
}

// outputTail is the end of a hook's output, where the reason for a
// failure usually is.
func outputTail(b []byte) string {
	const max = 500
	s := strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
	if s == "" {
		return "no output"
	}
	if len(s) > max {
		s = "…" + strings.ToValidUTF8(s[len(s)-max:], "")
	}
	return s
}
