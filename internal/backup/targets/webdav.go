package targets

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
	// upload has no overall timeout (an archive upload takes as long as it
	// takes) and authenticates up front, so the body is never buffered
	// for an auth retry.
	upload *gowebdav.Client
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
	upload := gowebdav.NewAuthClient(cfg.URL, gowebdav.NewPreemptiveAuth(&basicAuth{user: cfg.Username, pw: cfg.Password}))
	return &WebDAV{cfg: cfg, client: client, upload: upload}, nil
}

func (w *WebDAV) Open(ctx context.Context, p string) (io.WriteCloser, error) {
	full := path.Join(w.cfg.Path, p)
	dir := path.Dir(full)
	_ = w.client.MkdirAll(dir, 0o755)
	// Content length -1 sends the body chunked, straight from the pipe.
	return startPipeUpload(func(r io.Reader) error {
		return w.upload.WriteStreamWithLength(full, r, -1, 0o644)
	}, func() { _ = w.client.Remove(full) }), nil
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

// basicAuth sends credentials with every request. gowebdav's negotiating
// authorizer copies each upload body into memory so it can resend it
// after a 401.
type basicAuth struct{ user, pw string }

func (b *basicAuth) Authorize(_ *http.Client, rq *http.Request, _ string) error {
	if b.user != "" || b.pw != "" {
		rq.SetBasicAuth(b.user, b.pw)
	}
	return nil
}

func (b *basicAuth) Verify(_ *http.Client, rs *http.Response, p string) (bool, error) {
	if rs.StatusCode == http.StatusUnauthorized {
		return false, gowebdav.NewPathError("Authorize", p, rs.StatusCode)
	}
	return false, nil
}

func (b *basicAuth) Clone() gowebdav.Authenticator { return b }
func (b *basicAuth) Close() error                  { return nil }
func (b *basicAuth) String() string                { return "basic " + b.user }

// StorageInfo queries WebDAV quota (RFC 4331). Returns 0,0 if not supported.
func (w *WebDAV) StorageInfo() (total, used int64, err error) {
	// gowebdav doesn't expose quota directly. Try a PROPFIND.
	// If it fails, return 0,0 (not all servers support quota).
	return 0, 0, nil
}
