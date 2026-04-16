package targets

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/studio-b12/gowebdav"
)

type WebDAVConfig struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Path     string `json:"path"`
}

type WebDAV struct {
	cfg    WebDAVConfig
	client *gowebdav.Client
}

func NewWebDAV(raw any) (*WebDAV, error) {
	var cfg WebDAVConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("webdav: url required")
	}
	if cfg.Path == "" {
		cfg.Path = "/backups"
	}
	client := gowebdav.NewClient(cfg.URL, cfg.Username, cfg.Password)
	client.SetTimeout(30 * time.Second)
	return &WebDAV{cfg: cfg, client: client}, nil
}

func (w *WebDAV) Open(ctx context.Context, p string) (io.WriteCloser, error) {
	full := path.Join(w.cfg.Path, p)
	dir := path.Dir(full)
	_ = w.client.MkdirAll(dir, 0o755)
	return &webdavWriter{client: w.client, path: full}, nil
}

func (w *WebDAV) Read(ctx context.Context, p string) (io.ReadCloser, error) {
	full := path.Join(w.cfg.Path, p)
	stream, err := w.client.ReadStream(full)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (w *WebDAV) List(ctx context.Context, prefix string) ([]Entry, error) {
	root := path.Join(w.cfg.Path, prefix)
	var out []Entry
	walkWebDAV(w.client, root, w.cfg.Path, &out)
	return out, nil
}

func walkWebDAV(client *gowebdav.Client, dir, base string, out *[]Entry) {
	files, err := client.ReadDir(dir)
	if err != nil {
		return
	}
	for _, f := range files {
		full := path.Join(dir, f.Name())
		if f.IsDir() {
			walkWebDAV(client, full, base, out)
			continue
		}
		rel := full[len(base):]
		if len(rel) > 0 && rel[0] == '/' {
			rel = rel[1:]
		}
		*out = append(*out, Entry{
			Path:    rel,
			Size:    f.Size(),
			ModTime: f.ModTime(),
		})
	}
}

func (w *WebDAV) Delete(ctx context.Context, p string) error {
	return w.client.Remove(path.Join(w.cfg.Path, p))
}

// StorageInfo queries WebDAV quota (RFC 4331). Returns 0,0 if not supported.
func (w *WebDAV) StorageInfo() (total, used int64, err error) {
	// gowebdav doesn't expose quota directly. Try a PROPFIND.
	// If it fails, return 0,0 (not all servers support quota).
	return 0, 0, nil
}

// webdavWriter buffers the entire write and flushes on Close because
// gowebdav doesn't support streaming writes (WriteStream takes a reader).
type webdavWriter struct {
	client *gowebdav.Client
	path   string
	buf    bytes.Buffer
}

func (w *webdavWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *webdavWriter) Close() error {
	return w.client.WriteStream(w.path, &w.buf, 0o644)
}
