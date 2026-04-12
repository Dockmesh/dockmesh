package stacks

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Manager is the filesystem-backed stack registry.
// Layout: <root>/<name>/compose.yaml  (+ optional .env, .dockmesh.meta.json).
// The filesystem is the source of truth; the DB only holds metadata.
type Manager struct {
	root    string
	rootAbs string
	watcher *fsnotify.Watcher

	mu     sync.RWMutex
	stacks map[string]*Stack
}

type Stack struct {
	Name        string `json:"name"`
	ComposePath string `json:"compose_path"`
}

type Detail struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
}

var (
	ErrNotFound    = errors.New("stack not found")
	ErrExists      = errors.New("stack already exists")
	ErrInvalidName = errors.New("invalid stack name")
	ErrReserved    = errors.New("stack name is reserved")
	ErrPathEscape  = errors.New("stack path escapes root")
)

// Name validation per concept §15.5 (DNS-label-ish).
var stackNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

var reservedNames = map[string]bool{
	"dockmesh": true,
	"agent":    true,
	"system":   true,
	"api":      true,
	"admin":    true,
	"config":   true,
}

// ValidateName enforces the concept §15.5 naming rules.
func ValidateName(name string) error {
	if len(name) < 2 || len(name) > 63 {
		return fmt.Errorf("%w: length must be 2..63", ErrInvalidName)
	}
	if !stackNameRe.MatchString(name) {
		return fmt.Errorf("%w: must match [a-z0-9][a-z0-9-]*[a-z0-9]", ErrInvalidName)
	}
	if reservedNames[name] {
		return ErrReserved
	}
	return nil
}

func NewManager(root string) (*Manager, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	m := &Manager{
		root:    root,
		rootAbs: rootAbs,
		watcher: w,
		stacks:  make(map[string]*Stack),
	}
	if err := m.scan(); err != nil {
		return nil, err
	}
	if err := w.Add(root); err != nil {
		return nil, fmt.Errorf("watch %s: %w", root, err)
	}
	go m.watch()
	return m, nil
}

func (m *Manager) scan() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if err := ValidateName(name); err != nil {
			continue
		}
		compose := filepath.Join(m.root, name, "compose.yaml")
		if _, err := os.Stat(compose); err == nil {
			m.stacks[name] = &Stack{Name: name, ComposePath: compose}
		}
	}
	return nil
}

func (m *Manager) watch() {
	for {
		select {
		case ev, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			slog.Debug("stack fs event", "op", ev.Op.String(), "name", ev.Name)
			// TODO(phase1): reconcile on change + push WS notification.
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("stack watcher error", "err", err)
		}
	}
}

// safeDir validates the name and returns the absolute stack directory,
// guaranteeing it stays under the manager root.
func (m *Manager) safeDir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir := filepath.Clean(filepath.Join(m.rootAbs, name))
	if dir != filepath.Join(m.rootAbs, name) || !strings.HasPrefix(dir, m.rootAbs+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}
	return dir, nil
}

// Dir returns the absolute stack directory after validating the name and
// guaranteeing the path stays under the manager root.
func (m *Manager) Dir(name string) (string, error) {
	return m.safeDir(name)
}

func (m *Manager) List() []*Stack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Stack, 0, len(m.stacks))
	for _, s := range m.stacks {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Get(name string) (*Detail, error) {
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	compose, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	envBytes, _ := os.ReadFile(filepath.Join(dir, ".env"))
	return &Detail{Name: name, Compose: string(compose), Env: string(envBytes)}, nil
}

func (m *Manager) Create(name, compose, env string) (*Detail, error) {
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, ErrExists
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		return nil, err
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	m.stacks[name] = &Stack{Name: name, ComposePath: composePath}
	m.mu.Unlock()
	return &Detail{Name: name, Compose: compose, Env: env}, nil
}

func (m *Manager) Update(name, compose, env string) (*Detail, error) {
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644); err != nil {
		return nil, err
	}
	envPath := filepath.Join(dir, ".env")
	if env != "" {
		if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
			return nil, err
		}
	} else {
		_ = os.Remove(envPath)
	}
	return &Detail{Name: name, Compose: compose, Env: env}, nil
}

func (m *Manager) Delete(name string) error {
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.stacks, name)
	m.mu.Unlock()
	return nil
}

func (m *Manager) Close() error {
	return m.watcher.Close()
}
