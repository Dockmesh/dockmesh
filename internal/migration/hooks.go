package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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

// executeHook runs a shell command via docker exec on the first running
// container of the stack on the given host. This is how pre_dump and
// post_restore hooks are executed.
func (s *Service) executeHook(ctx context.Context, h host.Host, stackName, hookName, cmd string) error {
	if cmd == "" {
		return nil
	}
	slog.Info("migration hook executing",
		"stack", stackName, "hook", hookName, "cmd", cmd)

	// Find the first running container for this stack.
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

	// Execute via host's StartExec.
	session, err := h.StartExec(ctx, containerID, []string{"sh", "-c", cmd})
	if err != nil {
		return fmt.Errorf("hook %s: exec start: %w", hookName, err)
	}
	defer session.Close()

	// Read output (but don't block forever).
	buf := make([]byte, 64*1024)
	var output []byte
	for {
		n, rerr := session.Read(buf)
		if n > 0 {
			output = append(output, buf[:n]...)
		}
		if rerr != nil {
			break
		}
		if len(output) > 1024*1024 {
			break // cap at 1MB
		}
	}
	slog.Info("migration hook completed",
		"stack", stackName, "hook", hookName,
		"output_bytes", len(output))
	return nil
}
