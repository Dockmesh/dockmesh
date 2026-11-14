package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// HookCommands are the hook commands a migration of a stack would run, as
// shown for confirmation. Digest names exactly these commands; a caller
// confirms by sending it back.
type HookCommands struct {
	PreDump     string `json:"pre_dump,omitempty"`
	PostRestore string `json:"post_restore,omitempty"`
	Digest      string `json:"digest"`
}

func hookCommands(h *MigrationHooks) *HookCommands {
	if h == nil || (h.PreDump == "" && h.PostRestore == "") {
		return nil
	}
	b, _ := json.Marshal([]string{h.PreDump, h.PostRestore})
	sum := sha256.Sum256(b)
	return &HookCommands{PreDump: h.PreDump, PostRestore: h.PostRestore, Digest: hex.EncodeToString(sum[:])}
}

// HookCommands returns the hook commands in the stack's meta file, nil
// when it has none.
func (s *Service) HookCommands(stackName string) (*HookCommands, error) {
	h, err := s.loadHooks(stackName)
	if err != nil {
		return nil, fmt.Errorf("read migration hooks: %w", err)
	}
	return hookCommands(h), nil
}

// ErrHooksUnconfirmed matches a HooksUnconfirmedError.
var ErrHooksUnconfirmed = errors.New("the migration would run hook commands that were not confirmed")

// HooksUnconfirmedError refuses a migration whose hook commands the
// caller didn't confirm, or that changed since they were shown.
type HooksUnconfirmedError struct {
	Stack string
	Hooks *HookCommands
}

func (e *HooksUnconfirmedError) Error() string {
	return fmt.Sprintf("stack %s: %s", e.Stack, ErrHooksUnconfirmed)
}

func (e *HooksUnconfirmedError) Is(target error) bool { return target == ErrHooksUnconfirmed }

// checkHooksConfirmed returns the stack's hook commands when confirm is
// their digest, or a HooksUnconfirmedError.
func (s *Service) checkHooksConfirmed(stackName, confirm string) (*HookCommands, error) {
	cmds, err := s.HookCommands(stackName)
	if err != nil {
		return nil, err
	}
	if cmds != nil && cmds.Digest != confirm {
		return nil, &HooksUnconfirmedError{Stack: stackName, Hooks: cmds}
	}
	return cmds, nil
}
