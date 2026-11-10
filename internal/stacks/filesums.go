package stacks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// MetaFileName holds a stack's scaling rules, migration hooks and default
// environment override.
const MetaFileName = ".dockmesh.meta.json"

// trackedFiles are the stack files a deploy reads, besides the
// compose.<env>.yaml override files.
var trackedFiles = []string{"compose.yaml", ".env", ".env.age", MetaFileName}

// overrideFile matches the compose.<env>.yaml override files.
var overrideFile = regexp.MustCompile(`^compose\.[a-z0-9][a-z0-9_-]*\.yaml$`)

// backfillRow marks a stack whose sums predate the tracking of the meta
// and override files; RecordUntrackedSums fills those in once.
const backfillRow = "@backfill"

// ErrModifiedOutside refuses a deploy from stack files that changed since
// Dockmesh last wrote them, for example through a container that mounts
// the stack directory.
var ErrModifiedOutside = errors.New("stack files changed outside Dockmesh; review the files and confirm the deploy")

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

func (s *SumStore) deleteRow(stack, file string) error {
	_, err := s.db.Exec(`DELETE FROM stack_file_sums WHERE stack = ? AND file = ?`, stack, file)
	return err
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
			Title:    i18n.Msg("notice.git_deploy_held.title", name),
			Body:     i18n.Msg("notice.git_deploy_held.body", files),
			Link:     "/stacks/" + name,
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

// maxTrackedSize caps how much of a stack file is read. Larger files and
// anything that isn't a regular file (a FIFO, a device, a directory) get a
// checksum that never matches, so they count as changed without being read.
const maxTrackedSize = 16 << 20

const unreadableSum = "unreadable"

// readTracked returns the content of path and its hex SHA-256, nil and ""
// when it does not exist.
func readTracked(path string) ([]byte, string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxTrackedSize {
		return nil, unreadableSum, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTrackedSize+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxTrackedSize {
		return nil, unreadableSum, nil
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

// fileSum is the hex SHA-256 of path, or "" when it does not exist.
func fileSum(path string) (string, error) {
	_, sum, err := readTracked(path)
	return sum, err
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
	return slices.Contains(trackedFiles, file) || overrideFile.MatchString(file)
}

// filesOf lists the tracked files of a stack: the fixed ones, every
// override file on disk and every one recorded before, so a removed or
// newly added override file counts as a change too.
func filesOf(dir string, recorded map[string]string) ([]string, error) {
	files := slices.Clone(trackedFiles)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && overrideFile.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	for f := range recorded {
		if overrideFile.MatchString(f) && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
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
		return nil, m.recordSums(name)
	}
	files, err := filesOf(dir, recorded)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range files {
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		if sum != recorded[f] || sum == unreadableSum {
			changed = append(changed, f)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// AcceptExternal records the stack files as they are now, after an
// operator reviewed and confirmed them.
func (m *Manager) AcceptExternal(ctx context.Context, name string) error {
	if err := m.recordSums(name); err != nil {
		return err
	}
	m.resolveGitDeployHold(ctx, name)
	return nil
}

func (m *Manager) recordSums(name string) error {
	if m.sums == nil {
		return nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	recorded, err := m.sums.get(name)
	if err != nil {
		return err
	}
	files, err := filesOf(dir, recorded)
	if err != nil {
		return err
	}
	for _, f := range files {
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if err := m.sums.set(name, f, sum); err != nil {
			return err
		}
	}
	return nil
}

// RecordUntrackedSums records the current files of every stack that has
// no checksums yet, and the files without a checksum of every stack
// marked for the backfill, so stacks from before an upgrade start out
// trusted.
func (m *Manager) RecordUntrackedSums() {
	if m.sums == nil {
		return
	}
	for _, s := range m.List() {
		if err := m.backfillSums(s.Name); err != nil {
			slog.Warn("record stack file checksums", "stack", s.Name, "err", err)
			continue
		}
		if _, err := m.ModifiedOutside(s.Name); err != nil {
			slog.Warn("record stack file checksums", "stack", s.Name, "err", err)
		}
	}
}

// backfillSums records the files without a checksum as they are now, once,
// for a stack carrying the backfill marker. Files with a checksum keep it,
// so a change made before the upgrade is still reported.
func (m *Manager) backfillSums(name string) error {
	recorded, err := m.sums.get(name)
	if err != nil {
		return err
	}
	if _, ok := recorded[backfillRow]; !ok {
		return nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	files, err := filesOf(dir, recorded)
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, ok := recorded[f]; ok {
			continue
		}
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if err := m.sums.set(name, f, sum); err != nil {
			return err
		}
	}
	return m.sums.deleteRow(name, backfillRow)
}

// WriteFile runs write, which changes the tracked file of stack name, and
// records the new checksum when the file held what Dockmesh last recorded.
// A file already changed outside Dockmesh stays reported as changed, so
// writing it does not confirm the outside change.
func (m *Manager) WriteFile(name, file string, write func() error) error {
	if !isTracked(file) {
		return fmt.Errorf("%s is not a tracked stack file", file)
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	path := filepath.Join(dir, file)
	clean := true
	if m.sums != nil {
		recorded, err := m.sums.get(name)
		if err != nil {
			return err
		}
		if len(recorded) > 0 {
			before, err := fileSum(path)
			if err != nil {
				return err
			}
			clean = before == recorded[file] && before != unreadableSum
		}
	}
	m.markSelfWrite(path)
	if err := write(); err != nil {
		return err
	}
	if clean {
		m.recordSum(path)
	}
	return nil
}

// overrideRow is the sum row holding the environment override a manual
// deploy last used, with the checksum of its file.
const overrideRow = "@override"

// ErrOverrideUnconfirmed refuses a deploy without a request (git
// auto-deploy, migration) whose environment override is not the one the
// last manual deploy used. Override files and the meta file naming the
// default one are not written by Dockmesh, so only a manual deploy
// confirms them.
var ErrOverrideUnconfirmed = errors.New("environment override changed since the last manual deploy; deploy the stack once to confirm it")

var overrideName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (m *Manager) overrideSum(name, override string) (string, error) {
	if override == "" {
		return "", nil
	}
	if !overrideName.MatchString(override) {
		return "", fmt.Errorf("invalid environment name %q", override)
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return "", err
	}
	sum, err := fileSum(filepath.Join(dir, "compose."+override+".yaml"))
	if err != nil {
		return "", err
	}
	return override + ":" + sum, nil
}

// RecordOverride stores the override a manual deploy applied.
func (m *Manager) RecordOverride(name, override string) error {
	if m.sums == nil {
		return nil
	}
	sum, err := m.overrideSum(name, override)
	if err != nil {
		return err
	}
	return m.sums.set(name, overrideRow, sum)
}

// CheckOverride refuses override for a deploy without a request unless the
// last manual deploy used it with the same file content. No override is
// always fine.
func (m *Manager) CheckOverride(name, override string) error {
	if m.sums == nil || override == "" {
		return nil
	}
	sum, err := m.overrideSum(name, override)
	if err != nil {
		return err
	}
	recorded, err := m.sums.get(name)
	if err != nil {
		return err
	}
	if recorded[overrideRow] != sum {
		return fmt.Errorf("%w: %s", ErrOverrideUnconfirmed, override)
	}
	return nil
}

// Reasons the auto-scaler leaves a stack alone.
const (
	PauseModifiedOutside     = "modified_outside"
	PauseOverrideUnconfirmed = "override_unconfirmed"
	PauseInvalidRules        = "invalid_rules"
)

// ScalerPause is why the auto-scaler does not scale a stack: files changed
// outside Dockmesh, a default override no manual deploy confirmed, or
// scaling rules that don't validate.
type ScalerPause struct {
	Reason   string   `json:"reason"`
	Files    []string `json:"files,omitempty"`
	Override string   `json:"override,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ScalerHeldNoticeKey is the notice raised while the auto-scaler leaves
// stack alone.
func ScalerHeldNoticeKey(stack string) string { return "stacks.scaler-held:" + stack }

// HoldScaler tells the stack's deployers that auto-scaling of name is
// paused and why.
func (m *Manager) HoldScaler(ctx context.Context, name string, p ScalerPause) {
	if m.notices == nil {
		return
	}
	var body i18n.Text
	switch p.Reason {
	case PauseModifiedOutside:
		body = i18n.Msg("notice.scaler_held.files", strings.Join(p.Files, ", "))
	case PauseOverrideUnconfirmed:
		body = i18n.Msg("notice.scaler_held.override", p.Override)
	default:
		body = i18n.Msg("notice.scaler_held.rules", p.Error)
	}
	if err := m.notices.Raise(ctx, notices.RaiseInput{
		Key:      ScalerHeldNoticeKey(name),
		Audience: notices.WithPermission(string(rbac.PermStacksDeploy)),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.scaler_held.title", name),
		Body:     body,
		Link:     "/stacks/" + name,
	}); err != nil {
		slog.Warn("auto-scaling paused notice", "stack", name, "err", err)
	}
}

// ReleaseScaler resolves the auto-scaling paused notice of name.
func (m *Manager) ReleaseScaler(ctx context.Context, name string) {
	if m.notices == nil {
		return
	}
	if err := m.notices.Resolve(ctx, ScalerHeldNoticeKey(name)); err != nil {
		slog.Warn("resolve auto-scaling paused notice", "stack", name, "err", err)
	}
}

// RecordFile records the current checksum of a tracked file that
// Dockmesh wrote itself, such as a file restored from a backup.
func (m *Manager) RecordFile(name, file string) error {
	if !isTracked(file) {
		return nil
	}
	dir, err := m.safeDir(name)
	if err != nil {
		return err
	}
	m.recordSum(filepath.Join(dir, file))
	return nil
}
