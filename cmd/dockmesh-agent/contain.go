package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// The agent runs with docker-group (root-equivalent) rights, so every
// name the server sends is re-validated here with the server's own rules
// before it reaches the filesystem or a bind mount.

// openStacksRoot opens <dataDir>/stacks as an os.Root, so the per-stack
// operations below cannot leave it even through symlinks.
func openStacksRoot(base string) (*os.Root, error) {
	dir, err := filepath.Abs(filepath.Join(base, "stacks"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("stacks dir: %w", err)
	}
	return os.OpenRoot(dir)
}

// syncStack writes the compose and meta files to the agent's local stacks
// directory so the agent can survive a server loss. The .env file is not
// mirrored: it may hold secrets, and deploys bring their own copy.
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
	if req.Meta != "" {
		if err := root.WriteFile(filepath.Join(req.Name, ".dockmesh.meta.json"), []byte(req.Meta), 0o640); err != nil {
			return fmt.Errorf("sync meta: %w", err)
		}
	}
	return nil
}

// hasMirroredEnv reports whether an older agent left a plaintext .env copy
// in any stack directory.
func hasMirroredEnv(base string) bool {
	matches, err := filepath.Glob(filepath.Join(base, "stacks", "*", ".env"))
	if err != nil {
		return false
	}
	for _, m := range matches {
		// A directory here is what Docker creates for a missing bind
		// source, not a copy.
		if fi, err := os.Lstat(m); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// removeMirroredEnv deletes the .env copy an older agent left for stack
// name, unless a container of the project bind-mounts it or a directory
// holding it: then someone relies on that file. binds are the bind sources
// of the deployed project and of its containers.
func removeMirroredEnv(base, name string, binds []string) error {
	if err := stacks.ValidateName(name); err != nil {
		return err
	}
	root, err := openStacksRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	rel := filepath.Join(name, ".env")
	if _, err := root.Lstat(rel); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	env := filepath.Join(root.Name(), rel)
	// Docker reports the real path of a bind source, which differs from
	// env when the data directory lies behind a symlink.
	realEnv, err := filepath.EvalSymlinks(env)
	if err != nil {
		realEnv = env
	}
	for _, src := range binds {
		cands := []string{filepath.Clean(src)}
		if real, err := filepath.EvalSymlinks(src); err == nil {
			cands = append(cands, real)
		}
		for _, c := range cands {
			for _, e := range []string{env, realEnv} {
				if rel, err := filepath.Rel(c, e); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					slog.Warn("kept the stack's old .env copy: a container bind-mounts it, and it is no longer updated",
						"stack", name, "bind", src)
					return nil
				}
			}
		}
	}
	return root.Remove(rel)
}

// deleteStack removes the agent's local copy of a stack's files. Bind data
// owned by another user goes through the helper, so an agent that does not
// run as root can delete it too.
func deleteStack(ctx context.Context, files *stackhelper.Helper, base, name string) error {
	if err := stacks.ValidateName(name); err != nil {
		return err
	}
	root, err := openStacksRoot(base)
	if err != nil {
		return err
	}
	dir := filepath.Join(root.Name(), name)
	_ = root.Close()
	return files.RemoveAll(ctx, root.Name(), dir)
}
