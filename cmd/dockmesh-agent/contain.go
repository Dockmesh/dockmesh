package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// The agent runs with docker-group (root-equivalent) rights, so every
// name the server sends is re-validated here with the server's own rules
// before it reaches the filesystem or a bind mount.

// dockerVolumeNameRe is Docker's own rule for named volumes. Anything
// else (notably an absolute path) would turn the helper container's bind
// into a host-path mount.
var dockerVolumeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

func validateVolumeName(name string) error {
	if len(name) > 255 || !dockerVolumeNameRe.MatchString(name) {
		return fmt.Errorf("invalid volume name %q", name)
	}
	return nil
}

func dataDir() string {
	return envOr("DOCKMESH_DATA_DIR", "/var/lib/dockmesh")
}

// openStacksRoot opens <dataDir>/stacks as an os.Root, so the per-stack
// operations below cannot leave it even through symlinks.
func openStacksRoot(base string) (*os.Root, error) {
	dir := filepath.Join(base, "stacks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("stacks dir: %w", err)
	}
	return os.OpenRoot(dir)
}

// syncStack writes the compose+env+meta files to the agent's local
// stacks directory so the agent can survive a server loss.
func syncStack(base string, req agents.StackSyncReq) error {
	if err := stacks.ValidateName(req.Name); err != nil {
		return err
	}
	root, err := openStacksRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()

	if err := root.MkdirAll(req.Name, 0o750); err != nil {
		return fmt.Errorf("sync mkdir: %w", err)
	}
	if err := root.WriteFile(filepath.Join(req.Name, "compose.yaml"), []byte(req.Compose), 0o640); err != nil {
		return fmt.Errorf("sync compose.yaml: %w", err)
	}
	envPath := filepath.Join(req.Name, ".env")
	if req.Env != "" {
		if err := root.WriteFile(envPath, []byte(req.Env), 0o640); err != nil {
			return fmt.Errorf("sync .env: %w", err)
		}
	} else if err := root.Remove(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sync remove .env: %w", err)
	}
	if req.Meta != "" {
		if err := root.WriteFile(filepath.Join(req.Name, ".dockmesh.meta.json"), []byte(req.Meta), 0o640); err != nil {
			return fmt.Errorf("sync meta: %w", err)
		}
	}
	return nil
}

// deleteStack removes the agent's local copy of a stack's files.
func deleteStack(base, name string) error {
	if err := stacks.ValidateName(name); err != nil {
		return err
	}
	root, err := openStacksRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(name)
}
