package stacks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ErrModifiedOnDisk refuses a save whose compose.yaml or .env changed on
// disk since the editor loaded them.
var ErrModifiedOnDisk = errors.New("the stack files changed on disk since they were loaded")

// ConflictError is the ErrModifiedOnDisk of one save: the files that
// differ and what is on disk now.
type ConflictError struct {
	Files   []string
	Current *Detail
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: %s", ErrModifiedOnDisk, strings.Join(e.Files, ", "))
}

func (e *ConflictError) Unwrap() error { return ErrModifiedOnDisk }

// SaveInput is one save of a stack's files. An empty version skips the
// check for that file. Force writes despite a conflict.
type SaveInput struct {
	Compose        string
	Env            string
	ComposeVersion string
	EnvVersion     string
	// KeepEnv leaves .env alone, for a caller that may not read it and so
	// can neither know its content nor be told whether it changed.
	KeepEnv bool
	Force   bool
}

// contentVersion hashes the decrypted content, so re-encrypting .env.age
// under a new key doesn't look like a change.
func contentVersion(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// Save writes the stack files unless one changed on disk since the caller
// loaded it, which returns a *ConflictError and writes nothing. It also
// returns the files a forced save overwrote.
func (m *Manager) Save(name string, in SaveInput) (*Detail, []string, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if in.KeepEnv {
		in.EnvVersion = ""
	}
	conflict, err := m.findConflict(name, in)
	if err != nil {
		return nil, nil, err
	}
	var overwritten []string
	if conflict != nil {
		if !in.Force {
			return nil, nil, conflict
		}
		overwritten = conflict.Files
	}
	env := &in.Env
	if in.KeepEnv {
		env = nil
	}
	d, err := m.update(name, in.Compose, env)
	if err != nil {
		return nil, nil, err
	}
	return d, overwritten, nil
}

// findConflict compares the files on disk with the versions the caller
// loaded. A file that already holds what is being saved is no conflict.
func (m *Manager) findConflict(name string, in SaveInput) (*ConflictError, error) {
	if in.ComposeVersion == "" && in.EnvVersion == "" {
		return nil, nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	compose := string(raw)
	current := &Detail{Name: name, Compose: compose, ComposeVersion: contentVersion(compose)}
	var files []string
	if in.ComposeVersion != "" && in.ComposeVersion != current.ComposeVersion && compose != in.Compose {
		files = append(files, "compose.yaml")
	}
	if in.EnvVersion != "" {
		env, err := m.readEnv(dir)
		switch {
		case err == nil:
			current.Env, current.EnvVersion = env, contentVersion(env)
			if in.EnvVersion != current.EnvVersion && env != in.Env {
				files = append(files, ".env")
			}
		case !in.Force:
			return nil, err
		default:
			// A .env.age that no longer decrypts must not block the save
			// that replaces it.
			slog.Warn("forced stack save: read current .env", "stack", name, "err", err)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	return &ConflictError{Files: files, Current: current}, nil
}
