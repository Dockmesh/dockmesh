package restore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/operations"
)

// Leftover is a copy of the files a system restore replaced: the data
// directory's files under <data>/.pre-restore/<time>, the stacks under
// <stacks>.pre-restore-<time>.
type Leftover struct {
	Path       string    `json:"path"`
	RestoredAt time.Time `json:"restored_at"`
	Bytes      int64     `json:"bytes"`
}

var stampPattern = regexp.MustCompile(`^\d{8}-\d{6}$`)

const stampLayout = "20060102-150405"

// leftoverDir is one leftover as a name below the directory that holds it.
type leftoverDir struct {
	parent string
	name   string
	stamp  time.Time
}

func leftoverDirs(cfg *config.Config) ([]leftoverDir, error) {
	var out []leftoverDir
	dataPre := filepath.Join(filepath.Dir(cfg.DBPath), PreRestoreDir)
	entries, err := os.ReadDir(dataPre)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if t, ok := parseStamp(e.Name()); ok && e.IsDir() {
			out = append(out, leftoverDir{parent: dataPre, name: e.Name(), stamp: t})
		}
	}
	stacks := filepath.Clean(cfg.StacksRoot)
	parent := filepath.Dir(stacks)
	prefix := filepath.Base(stacks) + PreRestoreDir + "-"
	entries, err = os.ReadDir(parent)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		rest, found := strings.CutPrefix(e.Name(), prefix)
		if !found || !e.IsDir() {
			continue
		}
		if t, ok := parseStamp(rest); ok {
			out = append(out, leftoverDir{parent: parent, name: e.Name(), stamp: t})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].stamp.Before(out[j].stamp) })
	return out, nil
}

func parseStamp(s string) (time.Time, bool) {
	if !stampPattern.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.Parse(stampLayout, s)
	return t, err == nil
}

// Leftovers lists the copies earlier system restores kept, oldest first.
func Leftovers(cfg *config.Config) ([]Leftover, error) {
	dirs, err := leftoverDirs(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]Leftover, 0, len(dirs))
	for _, d := range dirs {
		path := filepath.Join(d.parent, d.name)
		out = append(out, Leftover{Path: path, RestoredAt: d.stamp, Bytes: dirSize(path)})
	}
	return out, nil
}

// RemoveLeftovers deletes every copy Leftovers lists. Only directories
// with the names a restore creates are touched, each opened below its
// parent so a symlink can't point the removal elsewhere.
func RemoveLeftovers(cfg *config.Config) (int, error) {
	if info, err := Staged(cfg); err != nil {
		return 0, err
	} else if info != nil && info.PrevDir != "" {
		return 0, errors.New("a system restore is being applied")
	}
	dirs, err := leftoverDirs(cfg)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, d := range dirs {
		root, err := os.OpenRoot(d.parent)
		if err != nil {
			return removed, err
		}
		err = root.RemoveAll(d.name)
		_ = root.Close()
		if err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func dirSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a size estimate skips what it can't read
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// LeftoverNoticeKey is the admin notice shown while leftovers exist.
const LeftoverNoticeKey = "restore.pre-restore-kept"

// SyncLeftoverNotice tells admins about the copies a system restore kept:
// they hold the old database and secret key and use disk space until
// someone deletes them.
func SyncLeftoverNotice(ctx context.Context, cfg *config.Config, n *notices.Service) error {
	list, err := Leftovers(cfg)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("A system restore kept the files it replaced. They include the old database and secret key. Delete them on the Backups page once the restored install works.\n")
	for _, l := range list {
		fmt.Fprintf(&b, "- %s (%s, %s)\n", l.Path, l.RestoredAt.Format("2006-01-02 15:04 UTC"), operations.HumanBytes(l.Bytes))
	}
	return n.Set(ctx, len(list) > 0, notices.RaiseInput{
		Key:      LeftoverNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevInfo,
		Title:    "Files from before the system restore are kept",
		Body:     strings.TrimSpace(b.String()),
		Link:     "/backups",
	})
}
