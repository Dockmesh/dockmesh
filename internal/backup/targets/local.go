package targets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/safepath"
)

type LocalConfig struct {
	Path string `json:"path"`
}

// LocalRootsSetting holds the directories local targets may write under,
// one per line. LocalRootsSeededSetting records the non-default roots the
// upgrade added from existing jobs, until an admin edits the list.
const (
	LocalRootsSetting       = "backups.local_roots"
	LocalRootsSeededSetting = "backups.local_roots_seeded"
	DefaultLocalPath        = "./data/backups"
)

// ErrLocalPathNotAllowed is returned for a local target outside every
// allowed root.
var ErrLocalPathNotAllowed = errors.New("local backup path is outside the allowed backup directories (Settings: " + LocalRootsSetting + ")")

var localRoots func() []string

// SetLocalRoots installs the source of allowed roots. Without it, any path
// is accepted.
func SetLocalRoots(f func() []string) { localRoots = f }

// ParseRoots splits the setting value into absolute, cleaned paths.
func ParseRoots(v string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == ',' }) {
		if line = strings.TrimSpace(line); line != "" {
			if abs, err := filepath.Abs(line); err == nil {
				out = append(out, abs)
			}
		}
	}
	return out
}

// CheckLocalConfig rejects a local target config whose path is outside
// the allowed roots, so such a path is never stored.
func CheckLocalConfig(raw any) error {
	_, err := NewLocal(raw)
	return err
}

// Within reports whether p is root or lies below it (lexically).
func Within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// locate splits p into the allowed root it lies in and the path below it.
func locate(p string) (string, string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", "", err
	}
	if localRoots == nil {
		return abs, ".", nil
	}
	for _, root := range localRoots() {
		if Within(root, abs) {
			rel, err := filepath.Rel(root, abs)
			return root, rel, err
		}
	}
	return "", "", ErrLocalPathNotAllowed
}

// Local writes below base/rel. Every access opens base as an os.Root, so a
// symlink anywhere below the allowed root can't send a backup elsewhere.
type Local struct {
	base, rel string
}

// NewLocal builds a local-disk target. The configured path is created
// if missing (mode 0700 — backups can hold sensitive data).
func NewLocal(raw any) (*Local, error) {
	var cfg LocalConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Path == "" {
		cfg.Path = DefaultLocalPath
	}
	base, rel, err := locate(cfg.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := r.MkdirAll(rel, 0o700); err != nil {
		if safepath.IsRootEscape(err) {
			return nil, ErrLocalPathNotAllowed
		}
		return nil, err
	}
	return &Local{base: base, rel: rel}, nil
}

func (l *Local) root() (*os.Root, error) {
	r, err := os.OpenRoot(l.base)
	if err != nil {
		return nil, err
	}
	if l.rel == "." {
		return r, nil
	}
	defer r.Close()
	return r.OpenRoot(l.rel)
}

type rootFile struct {
	*os.File
	root *os.Root
}

func (f rootFile) Close() error {
	err := f.File.Close()
	f.root.Close()
	return err
}

func (l *Local) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	r, err := l.root()
	if err != nil {
		return nil, err
	}
	name := filepath.FromSlash(path)
	if err := r.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		r.Close()
		return nil, err
	}
	f, err := r.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		r.Close()
		return nil, err
	}
	return rootFile{f, r}, nil
}

func (l *Local) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	r, err := l.root()
	if err != nil {
		return nil, err
	}
	f, err := r.Open(filepath.FromSlash(path))
	if err != nil {
		r.Close()
		return nil, err
	}
	return rootFile{f, r}, nil
}

func (l *Local) List(ctx context.Context, prefix string) ([]Entry, error) {
	r, err := l.root()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	start := strings.Trim(prefix, "/")
	if start == "" {
		start = "."
	}
	var out []Entry
	err = fs.WalkDir(r.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, Entry{Path: p, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (l *Local) Delete(ctx context.Context, path string) error {
	r, err := l.root()
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Remove(filepath.FromSlash(path))
}

// StorageInfo reports (total_bytes, used_bytes) for the volume hosting
// the target path. Used by the test-connect endpoint so the UI can show
// actual disk free instead of 0 bytes. "used" is total-free so the whole
// filesystem is accounted for, which is honest when the backup dir
// shares the fs with the rest of the server.
func (l *Local) StorageInfo() (int64, int64, error) {
	return diskUsage(filepath.Join(l.base, l.rel))
}

// decodeConfig converts an arbitrary `any` (most often a map[string]any
// from the JSON column) into a typed config struct without going through
// reflection or third-party decoders.
func decodeConfig(raw any, out any) error {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// HasPrefix is a tiny helper kept here so other targets can share it.
func HasPrefix(s, p string) bool { return strings.HasPrefix(s, p) }
