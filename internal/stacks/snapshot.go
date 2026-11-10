package stacks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot is the stack files read once. Detail and Meta hold the bytes
// whose checksums were compared, so a caller that builds containers from
// them uses exactly what was checked, not a later read a container could
// have swapped in between.
type Snapshot struct {
	Name   string
	Detail *Detail
	// Meta is the content of the meta file, nil when there is none.
	Meta []byte
	// Changed lists the tracked files that differ from what Dockmesh
	// recorded.
	Changed []string
	sums    map[string]string
}

// Snapshot reads the tracked files of stack name once and compares them
// with the recorded checksums. A stack without any recorded checksum is
// recorded as it is now, as ModifiedOutside does.
func (m *Manager) Snapshot(name string) (*Snapshot, error) {
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	var recorded map[string]string
	if m.sums != nil {
		if recorded, err = m.sums.get(name); err != nil {
			return nil, err
		}
	}
	files, err := filesOf(dir, recorded)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Name: name, sums: map[string]string{}}
	content := map[string][]byte{}
	for _, f := range files {
		b, sum, err := readTracked(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		if sum == unreadableSum && (f == "compose.yaml" || f == ".env" || f == ".env.age" || f == MetaFileName) {
			return nil, fmt.Errorf("%s of stack %s is not a regular file or too large", f, name)
		}
		snap.sums[f] = sum
		content[f] = b
		if m.sums != nil && len(recorded) > 0 && (sum != recorded[f] || sum == unreadableSum) {
			snap.Changed = append(snap.Changed, f)
		}
	}
	sort.Strings(snap.Changed)
	if m.sums != nil && len(recorded) == 0 {
		if err := m.recordSnapshot(snap); err != nil {
			return nil, err
		}
	}
	compose, ok := content["compose.yaml"]
	if !ok || compose == nil {
		return nil, ErrNotFound
	}
	env, err := m.envFrom(content[".env.age"], content[".env"])
	if err != nil {
		return nil, err
	}
	snap.Detail = &Detail{
		Name:           name,
		Compose:        string(compose),
		Env:            env,
		ComposeVersion: contentVersion(string(compose)),
		EnvVersion:     contentVersion(env),
	}
	if strings.TrimSpace(snap.Detail.Compose) == "" {
		snap.Detail.Status = "needs_recovery"
	}
	snap.Meta = content[MetaFileName]
	return snap, nil
}

// envFrom is readEnv on bytes already read.
func (m *Manager) envFrom(age, plain []byte) (string, error) {
	if age != nil {
		if m.secrets == nil || !m.secrets.Enabled() {
			return "", errors.New("found .env.age but secrets service is disabled")
		}
		pt, err := m.secrets.Decrypt(age)
		if err != nil {
			return "", fmt.Errorf("decrypt .env.age: %w", err)
		}
		return string(pt), nil
	}
	return string(plain), nil
}

// AcceptSnapshot records the files of snap as reviewed: the checksums of
// the bytes the snapshot holds, not of what is on disk by now.
func (m *Manager) AcceptSnapshot(ctx context.Context, snap *Snapshot) error {
	if err := m.recordSnapshot(snap); err != nil {
		return err
	}
	m.resolveGitDeployHold(ctx, snap.Name)
	return nil
}

func (m *Manager) recordSnapshot(snap *Snapshot) error {
	if m.sums == nil {
		return nil
	}
	for f, sum := range snap.sums {
		if err := m.sums.set(snap.Name, f, sum); err != nil {
			return err
		}
	}
	return nil
}
