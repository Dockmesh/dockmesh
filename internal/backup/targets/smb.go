package targets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/cloudsoda/go-smb2"
)

type SMBConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Password string `json:"password"`
	Path     string `json:"path"` // path within the share
}

type SMB struct {
	cfg SMBConfig
}

func NewSMB(raw any) (*SMB, error) {
	var cfg SMBConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Host == "" || cfg.Share == "" {
		return nil, fmt.Errorf("smb: host and share required")
	}
	if cfg.Port == 0 {
		cfg.Port = 445
	}
	if cfg.Path == "" {
		cfg.Path = "backups"
	}
	return &SMB{cfg: cfg}, nil
}

func (s *SMB) connect(ctx context.Context) (*smb2.Share, *smb2.Session, net.Conn, error) {
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	conn, err := dialTCP(addr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("smb: dial: %w", err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     s.cfg.Username,
			Password: s.cfg.Password,
		},
	}
	session, err := d.DialConn(ctx, conn, addr)
	if err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("smb: session: %w", err)
	}

	share, err := session.Mount(s.cfg.Share)
	if err != nil {
		session.Logoff()
		conn.Close()
		return nil, nil, nil, fmt.Errorf("smb: mount %s: %w", s.cfg.Share, err)
	}
	return share, session, conn, nil
}

func (s *SMB) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	share, session, conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	full = strings.ReplaceAll(full, "/", "\\")
	// filepath.Dir doesn't split on backslashes on Linux, so the parent
	// comes from the slash form.
	dir := strings.ReplaceAll(pathpkg.Dir(filepath.ToSlash(filepath.Join(s.cfg.Path, path))), "/", "\\")
	if err := share.MkdirAll(dir, 0o755); err != nil {
		_ = share.Umount()
		_ = session.Logoff()
		_ = conn.Close()
		return nil, fmt.Errorf("smb: create %s: %w", dir, err)
	}
	f, err := share.Create(full)
	if err != nil {
		share.Umount()
		session.Logoff()
		conn.Close()
		return nil, fmt.Errorf("smb: create: %w", err)
	}
	return &smbWriter{f: f, share: share, session: session, conn: conn}, nil
}

func (s *SMB) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	share, session, conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	full = strings.ReplaceAll(full, "/", "\\")
	f, err := share.Open(full)
	if err != nil {
		share.Umount()
		session.Logoff()
		conn.Close()
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		share.Umount()
		session.Logoff()
		conn.Close()
		return nil, fmt.Errorf("smb: stat %s: %w", full, err)
	}
	return &smbReader{r: sizedReader{r: f, remaining: fi.Size()}, f: f, share: share, session: session, conn: conn}, nil
}

func (s *SMB) List(ctx context.Context, prefix string) ([]Entry, error) {
	share, session, conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer share.Umount()
	defer session.Logoff()
	defer conn.Close()

	root := filepath.Join(s.cfg.Path, filepath.FromSlash(prefix))
	root = strings.ReplaceAll(root, "/", "\\")
	var out []Entry
	walkSMB(share, root, s.cfg.Path, &out)
	return out, nil
}

func walkSMB(share *smb2.Share, dir, base string, out *[]Entry) {
	entries, err := share.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walkSMB(share, full, base, out)
			continue
		}
		rel, _ := filepath.Rel(base, full)
		*out = append(*out, Entry{
			Path:    filepath.ToSlash(rel),
			Size:    e.Size(),
			ModTime: e.ModTime(),
		})
	}
}

func (s *SMB) Delete(ctx context.Context, path string) error {
	share, session, conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer share.Umount()
	defer session.Logoff()
	defer conn.Close()
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	return share.Remove(strings.ReplaceAll(full, "/", "\\"))
}

func (s *SMB) StorageInfo() (total, used int64, err error) {
	share, session, conn, err := s.connect(context.Background())
	if err != nil {
		return 0, 0, err
	}
	defer share.Umount()
	defer session.Logoff()
	defer conn.Close()
	stat, err := share.Statfs(".")
	if err != nil {
		return 0, 0, err
	}
	total = int64(stat.TotalBlockCount() * stat.BlockSize())
	free := int64(stat.AvailableBlockCount() * stat.BlockSize())
	used = total - free
	return total, used, nil
}

// ListSMBShares connects to an SMB server and returns available share names.
func ListSMBShares(host string, port int, username, password string) ([]string, error) {
	if port == 0 {
		port = 445
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := dialTCP(addr)
	if err != nil {
		return nil, fmt.Errorf("smb: dial: %w", err)
	}
	defer conn.Close()

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}
	session, err := d.DialConn(context.Background(), conn, addr)
	if err != nil {
		return nil, fmt.Errorf("smb: auth failed: %w", err)
	}
	defer session.Logoff()

	names, err := session.ListSharenames()
	if err != nil {
		return nil, fmt.Errorf("smb: list shares: %w", err)
	}
	// Filter out system shares (IPC$, ADMIN$, C$, etc.)
	var out []string
	for _, n := range names {
		if strings.HasSuffix(n, "$") {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

type smbWriter struct {
	closed  bool
	f       *smb2.File
	share   *smb2.Share
	session *smb2.Session
	conn    net.Conn
}

func (w *smbWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

// Close reports the file's close (the last write reaching the share).
// Logoff closes the connection, so net.ErrClosed from conn.Close is ignored.
func (w *smbWriter) Close() error {
	// go-smb2 blocks on a second logoff of the same session.
	if w.closed {
		return nil
	}
	w.closed = true
	err := w.f.Close()
	_ = w.share.Umount()
	_ = w.session.Logoff()
	if cerr := w.conn.Close(); err == nil && !errors.Is(cerr, net.ErrClosed) {
		err = cerr
	}
	return err
}

type smbReader struct {
	closed  bool
	r       sizedReader
	f       *smb2.File
	share   *smb2.Share
	session *smb2.Session
	conn    net.Conn
}

func (r *smbReader) Read(p []byte) (int, error) { return r.r.Read(p) }
func (r *smbReader) Close() error {
	// go-smb2 blocks on a second logoff of the same session.
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.f.Close()
	_ = r.share.Umount()
	_ = r.session.Logoff()
	if cerr := r.conn.Close(); err == nil && !errors.Is(cerr, net.ErrClosed) {
		err = cerr
	}
	return err
}

// sizedReader stops at the size the file had when it was opened instead
// of asking the server for the end of file. ksmbd's reply to a READ at
// the end of a file carries a signature go-smb2 rejects, so the final
// read fails instead of returning io.EOF and age then drops the last
// chunk of an encrypted archive.
type sizedReader struct {
	r         io.Reader
	remaining int64
}

func (s *sizedReader) Read(p []byte) (int, error) {
	if s.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > s.remaining {
		p = p[:s.remaining]
	}
	n, err := s.r.Read(p)
	if n < 0 {
		n = 0
	}
	s.remaining -= int64(n)
	if errors.Is(err, io.EOF) && s.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
