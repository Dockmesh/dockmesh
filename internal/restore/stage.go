package restore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
)

// A system restore is applied in two steps. Stage extracts the archive
// next to the live files and checks it while the server keeps running;
// ApplyStaged, run at startup before anything opens the database, swaps
// the staged files in with renames. Every rename is atomic and the swap
// picks up where it stopped if the process dies halfway, so the install
// is never left half old, half new. The replaced files are kept under
// PreRestoreDir.
const (
	StagingDir    = ".restore-staged"
	PreRestoreDir = ".pre-restore"
	stagedMarker  = "READY"
)

// ErrNothingStaged is returned by DiscardStaged when no restore waits.
var ErrNothingStaged = errors.New("no staged restore")

// StagedInfo describes a restore waiting for the next start.
type StagedInfo struct {
	StagedAt time.Time `json:"staged_at"`
	Source   string    `json:"source"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	// Set once the swap has started; the replaced files go here.
	PrevDir    string `json:"prev_dir,omitempty"`
	PrevStacks string `json:"prev_stacks,omitempty"`
}

type stagePaths struct {
	root   string // <data>/.restore-staged
	data   string // staged data dir, incl. the DB
	stacks string // <stacks root>.restore-staged, on the stacks' filesystem
	cfg    *config.Config
}

func pathsFor(cfg *config.Config) stagePaths {
	dataDir := filepath.Dir(cfg.DBPath)
	root := filepath.Join(dataDir, StagingDir)
	p := stagePaths{
		root:   root,
		data:   filepath.Join(root, "data"),
		stacks: filepath.Clean(cfg.StacksRoot) + StagingDir,
	}
	staged := func(live string) string {
		if live == "" {
			return ""
		}
		return filepath.Join(p.data, filepath.Base(live))
	}
	p.cfg = &config.Config{
		DBPath:           staged(cfg.DBPath),
		StacksRoot:       p.stacks,
		SecretsPath:      staged(cfg.SecretsPath),
		SecretsKeyPath:   staged(cfg.SecretsKeyPath),
		AuditGenesisPath: staged(cfg.AuditGenesisPath),
	}
	return p
}

// StageOptions tune Stage. Verify runs after extraction and before the
// restore is marked ready, e.g. to compare the archive's hash.
type StageOptions struct {
	Verify func() error
	// Files writes and removes stack data of other owners; see
	// Options.Files.
	Files FileHelper
}

// Stage caps what an archive may unpack to, like verification does: the
// archive may come from an upload.
const (
	stageMaxBytes   = verifyMaxBytes
	stageMaxEntries = verifyMaxEntries
)

// Stage extracts a plaintext system archive into the staging area and
// runs the sanity checks on it. A failing check removes the staging
// area again and returns the result with an error.
func Stage(ctx context.Context, r io.Reader, cfg *config.Config, source string, opts StageOptions) (*Counts, *SanityResult, error) {
	p := pathsFor(cfg)
	drop := func() error { return clearStaging(ctx, p, opts.Files) }
	if err := drop(); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(p.data, 0o700); err != nil {
		return nil, nil, err
	}
	live := map[string]fileOwner{}
	if o, ok := ownerOf(filepath.Dir(cfg.DBPath)); ok {
		live[filepath.Dir(p.cfg.DBPath)] = o
	}
	stacksOwner, stacksKnown := ownerOf(cfg.StacksRoot)
	if stacksKnown {
		live[p.stacks] = stacksOwner
	}
	counts, err := Extract(ctx, r, p.cfg, Options{Force: true, TmpBase: p.root, MaxBytes: stageMaxBytes, MaxEntries: stageMaxEntries, Files: opts.Files, liveOwners: live})
	if err != nil {
		_ = drop()
		return nil, nil, err
	}
	// The stacks root itself is not in the archive; the restored one keeps
	// the mode and owner of the one it replaces.
	if fi, err := os.Stat(cfg.StacksRoot); err == nil && fi.IsDir() && exists(p.stacks) {
		err := os.Chmod(p.stacks, fi.Mode().Perm())
		if err == nil && stacksKnown && os.Geteuid() == 0 {
			err = os.Lchown(p.stacks, stacksOwner.uid, stacksOwner.gid)
		}
		if err != nil {
			_ = drop()
			return nil, nil, err
		}
	}
	if counts.LostOwners > 0 {
		slog.Warn("restore: stack data of other owners restored as the service's own, since Docker is unavailable", "entries", counts.LostOwners)
	}
	if opts.Verify != nil {
		if err := opts.Verify(); err != nil {
			_ = drop()
			return counts, nil, err
		}
	}
	result, err := Sanity(p.cfg)
	if err != nil {
		_ = drop()
		return counts, nil, err
	}
	if !result.Passed {
		_ = drop()
		return counts, result, fmt.Errorf("the backup failed the restore checks: %s", result.Summary)
	}
	info := StagedInfo{StagedAt: time.Now().UTC(), Source: source, Files: counts.Files, Bytes: counts.Bytes}
	if err := writeMarker(p, &info); err != nil {
		_ = drop()
		return counts, result, err
	}
	return counts, result, nil
}

// Staged reports the restore waiting for the next start, if any.
func Staged(cfg *config.Config) (*StagedInfo, error) {
	return readMarker(pathsFor(cfg))
}

// DiscardStaged drops a staged restore that has not been applied yet.
// files removes stack data of other owners; nil removes only what the
// service may.
func DiscardStaged(ctx context.Context, cfg *config.Config, files FileHelper) error {
	p := pathsFor(cfg)
	info, err := readMarker(p)
	if err != nil {
		return err
	}
	if info == nil {
		return ErrNothingStaged
	}
	if info.PrevDir != "" {
		return errors.New("the staged restore is already being applied")
	}
	return clearStaging(ctx, p, files)
}

// ApplyStaged swaps a staged restore in. Call it before opening the
// database. Returns false when nothing was staged.
func ApplyStaged(cfg *config.Config) (bool, error) {
	p := pathsFor(cfg)
	info, err := readMarker(p)
	if err != nil {
		return false, err
	}
	if info == nil {
		// An extraction that never finished leaves no marker.
		if err := clearStaging(context.Background(), p, nil); err != nil {
			slog.Warn("restore: remove unfinished staging area", "err", err)
		}
		return false, nil
	}
	dataDir := filepath.Dir(cfg.DBPath)
	if info.PrevDir == "" {
		stamp := time.Now().UTC().Format("20060102-150405")
		info.PrevDir = filepath.Join(dataDir, PreRestoreDir, stamp)
		info.PrevStacks = filepath.Clean(cfg.StacksRoot) + PreRestoreDir + "-" + stamp
		// Recorded before the first rename, so a resumed swap moves the
		// remaining live files to the same place.
		if err := writeMarker(p, info); err != nil {
			return false, err
		}
	}
	// The marker is writable by the service account; only the exact paths
	// this function names are accepted.
	stackStamp, stacksOK := strings.CutPrefix(info.PrevStacks, filepath.Clean(cfg.StacksRoot)+PreRestoreDir+"-")
	if info.PrevDir != filepath.Clean(info.PrevDir) || filepath.Dir(info.PrevDir) != filepath.Join(dataDir, PreRestoreDir) || !validStamp(filepath.Base(info.PrevDir)) ||
		!stacksOK || !validStamp(stackStamp) {
		return false, fmt.Errorf("restore: staging marker names unexpected paths %q, %q", info.PrevDir, info.PrevStacks)
	}
	if err := os.MkdirAll(info.PrevDir, 0o700); err != nil {
		return false, err
	}
	// Run as root, the directories holding the copies get the data
	// directory's owner, or the server could not list or delete them.
	if o, ok := ownerOf(dataDir); ok && os.Geteuid() == 0 {
		for _, d := range []string{filepath.Dir(info.PrevDir), info.PrevDir} {
			if err := chownDirNoFollow(d, o); err != nil {
				return false, err
			}
		}
	}

	// Database first, with the WAL and SHM files of the old one.
	db := filepath.Base(cfg.DBPath)
	if exists(filepath.Join(p.data, db)) {
		for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
			if err := moveAside(cfg.DBPath+suffix, filepath.Join(info.PrevDir, db+suffix)); err != nil {
				return false, err
			}
		}
		if err := os.Rename(filepath.Join(p.data, db), cfg.DBPath); err != nil {
			return false, fmt.Errorf("swap database: %w", err)
		}
	}

	entries, err := os.ReadDir(p.data)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, e := range entries {
		name := e.Name()
		// Local backups kept in the data dir stay where they are; older
		// system backups included them.
		if strings.HasPrefix(name, db) || name == StagingDir || name == PreRestoreDir || name == "backups" {
			continue
		}
		live := filepath.Join(dataDir, name)
		if err := moveAside(live, filepath.Join(info.PrevDir, name)); err != nil {
			return false, err
		}
		if err := os.Rename(filepath.Join(p.data, name), live); err != nil {
			return false, fmt.Errorf("swap %s: %w", name, err)
		}
	}

	if exists(p.stacks) {
		if err := moveAside(cfg.StacksRoot, info.PrevStacks); err != nil {
			return false, err
		}
		if err := os.Rename(p.stacks, cfg.StacksRoot); err != nil {
			return false, fmt.Errorf("swap stacks: %w", err)
		}
	}
	if err := os.RemoveAll(p.root); err != nil {
		return true, fmt.Errorf("remove staging area: %w", err)
	}
	slog.Info("restore: system backup applied", "source", info.Source, "previous_files", info.PrevDir, "previous_stacks", info.PrevStacks)
	return true, nil
}

// moveAside renames live to dst unless live is gone (already moved by an
// interrupted swap, or never there).
func moveAside(live, dst string) error {
	if !exists(live) {
		return nil
	}
	if exists(dst) {
		return fmt.Errorf("restore: %s and %s both exist; resolve by hand", live, dst)
	}
	if err := os.Rename(live, dst); err != nil {
		return fmt.Errorf("move %s aside: %w", live, err)
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func clearStaging(ctx context.Context, p stagePaths, files FileHelper) error {
	if err := os.RemoveAll(p.root); err != nil {
		return err
	}
	return removeTree(ctx, files, p.stacks)
}

// removeTree deletes dir, through files when it holds data the service
// may not delete itself.
func removeTree(ctx context.Context, files FileHelper, dir string) error {
	err := os.RemoveAll(dir)
	if err == nil || files == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	return files.RemoveAll(ctx, filepath.Dir(dir), dir)
}

func writeMarker(p stagePaths, info *StagedInfo) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.root, stagedMarker+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.root, stagedMarker))
}

func readMarker(p stagePaths) (*StagedInfo, error) {
	b, err := os.ReadFile(filepath.Join(p.root, stagedMarker))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var info StagedInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("staged restore marker: %w", err)
	}
	return &info, nil
}
