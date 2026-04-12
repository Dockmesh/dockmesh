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

	subsMu sync.Mutex
	subs   []chan Event
}

// Event is emitted when a stack file changes on disk from outside Dockmesh
// (external editor, git pull, rsync, etc.) — see concept §15.9.
type Event struct {
	Type string `json:"type"` // "modified" | "removed" | "created"
	Name string `json:"name"`
	File string `json:"file,omitempty"` // "compose.yaml" | ".env" | ""
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
	// Watch the root (for new/deleted stack dirs) and each existing stack dir.
	if err := w.Add(root); err != nil {
		return nil, fmt.Errorf("watch %s: %w", root, err)
	}
	for name := range m.stacks {
		_ = w.Add(filepath.Join(m.rootAbs, name))
	}
	go m.watch()
	return m, nil
}

// Subscribe returns a channel that receives external-change events and a
// function to unsubscribe. The channel is closed when the caller unsubscribes.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	m.subsMu.Lock()
	m.subs = append(m.subs, ch)
	m.subsMu.Unlock()
	return ch, func() {
		m.subsMu.Lock()
		defer m.subsMu.Unlock()
		for i, c := range m.subs {
			if c == ch {
				m.subs = append(m.subs[:i], m.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
}

func (m *Manager) publish(ev Event) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber — drop rather than block the watcher loop.
		}
	}
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
			m.handleFSEvent(ev)
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("stack watcher error", "err", err)
		}
	}
}

// handleFSEvent classifies a raw fsnotify event as either a stack-dir
// lifecycle event (create/remove in root) or a stack-file change (inside a
// stack dir) and publishes the result to subscribers.
func (m *Manager) handleFSEvent(ev fsnotify.Event) {
	abs, err := filepath.Abs(ev.Name)
	if err != nil {
		return
	}
	// Top-level event: a stack directory was created or removed.
	if filepath.Dir(abs) == m.rootAbs {
		name := filepath.Base(abs)
		if err := ValidateName(name); err != nil {
			return
		}
		switch {
		case ev.Op&fsnotify.Create != 0:
			_ = m.watcher.Add(abs)
			m.publish(Event{Type: "created", Name: name})
		case ev.Op&fsnotify.Remove != 0:
			_ = m.watcher.Remove(abs)
			m.mu.Lock()
			delete(m.stacks, name)
			m.mu.Unlock()
			m.publish(Event{Type: "removed", Name: name})
		}
		return
	}
	// File-level event inside a stack dir.
	parent := filepath.Dir(abs)
	if filepath.Dir(parent) != m.rootAbs {
		return
	}
	name := filepath.Base(parent)
	file := filepath.Base(abs)
	if file != "compose.yaml" && file != ".env" && file != ".dockmesh.meta.json" {
		return
	}
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 && ev.Op&fsnotify.Remove == 0 {
		return
	}
	typ := "modified"
	if ev.Op&fsnotify.Remove != 0 {
		typ = "removed"
	}
	m.publish(Event{Type: typ, Name: name, File: file})
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
	// Start watching the new dir so external edits also emit events.
	_ = m.watcher.Add(dir)
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
