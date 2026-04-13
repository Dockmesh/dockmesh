package targets

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type LocalConfig struct {
	Path string `json:"path"`
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
