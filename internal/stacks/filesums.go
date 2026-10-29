package stacks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// trackedFiles are the stack files a deploy reads.
var trackedFiles = []string{"compose.yaml", ".env", ".env.age"}

// ErrModifiedOutside refuses a deploy from stack files that changed since
// Dockmesh last wrote them, for example through a container that mounts
// the stack directory.
var ErrModifiedOutside = errors.New("compose.yaml or .env changed outside Dockmesh; review the files and confirm the deploy")

// SumStore keeps the checksums of the stack files as Dockmesh last wrote
// them. It lives in the database so a container that can write the stack
// directory cannot update it.
type SumStore struct{ db *sql.DB }

func NewSumStore(db *sql.DB) *SumStore { return &SumStore{db: db} }

func (s *SumStore) set(stack, file, sum string) error {
	_, err := s.db.Exec(`
		INSERT INTO stack_file_sums (stack, file, sum) VALUES (?, ?, ?)
		ON CONFLICT (stack, file) DO UPDATE SET sum = excluded.sum, updated_at = CURRENT_TIMESTAMP`,
		stack, file, sum)
	return err
}

func (s *SumStore) get(stack string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT file, sum FROM stack_file_sums WHERE stack = ?`, stack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var file, sum string
		if err := rows.Scan(&file, &sum); err != nil {
			return nil, err
		}
		out[file] = sum
	}
	return out, rows.Err()
}

func (s *SumStore) deleteStack(stack string) error {
	_, err := s.db.Exec(`DELETE FROM stack_file_sums WHERE stack = ?`, stack)
	return err
}

// SetSumStore enables tracking of changes made outside Dockmesh.
func (m *Manager) SetSumStore(s *SumStore) { m.sums = s }

// SetNotices lets a held-back git deploy tell the stack's deployers.
func (m *Manager) SetNotices(n *notices.Service) { m.notices = n }

// GitDeployHeldNoticeKey is the notice raised while a git deploy of stack
// waits for its outside changes to be confirmed.
func GitDeployHeldNoticeKey(stack string) string { return "stacks.git-deploy-held:" + stack }

// CheckGitDeploy refuses an automatic deploy from stack files changed
// outside Dockmesh. Nobody is there to confirm them, so it raises a
// notice asking a deployer to review and deploy the stack by hand.
func (m *Manager) CheckGitDeploy(ctx context.Context, name string) error {
	changed, err := m.ModifiedOutside(name)
	if err != nil || len(changed) == 0 {
		return err
	}
	files := strings.Join(changed, ", ")
	if m.notices != nil {
		if nerr := m.notices.Raise(ctx, notices.RaiseInput{
			Key:      GitDeployHeldNoticeKey(name),
			Audience: notices.WithPermission(string(rbac.PermStacksDeploy)),
			Severity: notices.SevWarning,
			Title:    "Git deploy of " + name + " is on hold",
			Body: "The new commit was not deployed because " + files + " of this stack changed " +
				"outside Dockmesh. Open the stack, review the files and deploy it to confirm the changes; " +
				"git deploys resume after that.",
			Link: "/stacks/" + name,
		}); nerr != nil {
			slog.Warn("git deploy held back notice", "stack", name, "err", nerr)
		}
	}
	return fmt.Errorf("%w: %s", ErrModifiedOutside, files)
}

// Deployed clears the held-back git deploy notice after a deploy, unless
// the stack files still differ from what Dockmesh recorded: a deploy that
// skipped the confirmation (a migration, for one) leaves git deploys held.
func (m *Manager) Deployed(ctx context.Context, name string) {
	if m.notices == nil {
		return
	}
	changed, err := m.ModifiedOutside(name)
	if err != nil {
		slog.Warn("check stack files after deploy", "stack", name, "err", err)
		return
	}
	if len(changed) == 0 {
		m.resolveGitDeployHold(ctx, name)
	}
}

func (m *Manager) resolveGitDeployHold(ctx context.Context, name string) {
	if m.notices == nil {
		return
	}
	if err := m.notices.Resolve(ctx, GitDeployHeldNoticeKey(name)); err != nil {
		slog.Warn("resolve git deploy held back notice", "stack", name, "err", err)
	}
}

// fileSum is the hex SHA-256 of path, or "" when it does not exist.
func fileSum(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// recordSum stores the checksum of a tracked file Dockmesh just wrote or
// removed.
func (m *Manager) recordSum(path string) {
	if m.sums == nil {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	name, file := filepath.Base(filepath.Dir(abs)), filepath.Base(abs)
	if !isTracked(file) {
		return
	}
	sum, err := fileSum(abs)
	if err == nil {
		err = m.sums.set(name, file, sum)
	}
	if err != nil {
		slog.Warn("record stack file checksum", "stack", name, "file", file, "err", err)
	}
}

func isTracked(file string) bool {
	for _, f := range trackedFiles {
		if f == file {
			return true
		}
	}
	return false
}

// ModifiedOutside lists the tracked files of a stack whose content differs
// from what Dockmesh last wrote. A stack without any recorded checksum
// (created before tracking, or by hand in the stacks directory) is
// recorded as it is now.
func (m *Manager) ModifiedOutside(name string) ([]string, error) {
	if m.sums == nil {
		return nil, nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return nil, err
	}
	recorded, err := m.sums.get(name)
	if err != nil {
		return nil, err
	}
	if len(recorded) == 0 {
		return nil, m.AcceptExternal(name)
	}
	var changed []string
	for _, f := range trackedFiles {
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		if sum != recorded[f] {
			changed = append(changed, f)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// AcceptExternal records the stack files as they are now, after an
// operator reviewed and confirmed them.
func (m *Manager) AcceptExternal(name string) error {
	if m.sums == nil {
		return nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	for _, f := range trackedFiles {
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if err := m.sums.set(name, f, sum); err != nil {
			return err
		}
	}
	m.resolveGitDeployHold(context.Background(), name)
	return nil
}

// RecordUntrackedSums records the current files of every stack that has
// no checksums yet, so stacks from before an upgrade start out trusted.
func (m *Manager) RecordUntrackedSums() {
	if m.sums == nil {
		return
	}
	for _, s := range m.List() {
		if _, err := m.ModifiedOutside(s.Name); err != nil {
			slog.Warn("record stack file checksums", "stack", s.Name, "err", err)
		}
	}
}
