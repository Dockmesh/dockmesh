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
// one per line.
const LocalRootsSetting = "backups.local_roots"

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

// allowedLocalPath returns p, resolved, if it lies inside an allowed root.
// Symlinks under the root are followed before the check, so a link can't
// point a target elsewhere.
func allowedLocalPath(p string) (string, error) {
	if localRoots == nil {
		return p, nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	for _, root := range localRoots() {
		if !safepath.Within(root, abs) {
			continue
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return "", err
		}
		resolved, err := safepath.Resolve(root, filepath.ToSlash(rel))
		if err != nil {
			return "", ErrLocalPathNotAllowed
		}
		return resolved, nil
	}
	return "", ErrLocalPathNotAllowed
}

type Local struct {
	cfg LocalConfig
}

// NewLocal builds a local-disk target. The configured path is created
// if missing (mode 0700 — backups can hold sensitive data).
func NewLocal(raw any) (*Local, error) {
	var cfg LocalConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Path == "" {
		cfg.Path = "./data/backups"
	}
	path, err := allowedLocalPath(cfg.Path)
	if err != nil {
		return nil, err
	}
	cfg.Path = path
	if err := os.MkdirAll(cfg.Path, 0o700); err != nil {
		return nil, err
	}
	return &Local{cfg: cfg}, nil
}

func (l *Local) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	full := filepath.Join(l.cfg.Path, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

func (l *Local) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(l.cfg.Path, filepath.FromSlash(path)))
}

func (l *Local) List(ctx context.Context, prefix string) ([]Entry, error) {
	root := filepath.Join(l.cfg.Path, filepath.FromSlash(prefix))
	var out []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
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
		rel, _ := filepath.Rel(l.cfg.Path, p)
		out = append(out, Entry{
			Path:    filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (l *Local) Delete(ctx context.Context, path string) error {
	return os.Remove(filepath.Join(l.cfg.Path, filepath.FromSlash(path)))
}

// StorageInfo reports (total_bytes, used_bytes) for the volume hosting
// the target path. Used by the test-connect endpoint so the UI can show
// actual disk free instead of 0 bytes. "used" is total-free so the whole
// filesystem is accounted for, which is honest when the backup dir
// shares the fs with the rest of the server.
func (l *Local) StorageInfo() (int64, int64, error) {
	return diskUsage(l.cfg.Path)
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
