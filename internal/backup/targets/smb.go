package targets

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"

	"github.com/hirochachacha/go-smb2"
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

func (s *SMB) connect() (*smb2.Share, *smb2.Session, net.Conn, error) {
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("smb: dial: %w", err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     s.cfg.Username,
			Password: s.cfg.Password,
		},
	}
	session, err := d.Dial(conn)
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
	share, session, conn, err := s.connect()
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	full = strings.ReplaceAll(full, "/", "\\")
	dir := filepath.Dir(full)
	_ = share.MkdirAll(dir, 0o755)
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
	share, session, conn, err := s.connect()
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
	return &smbReader{f: f, share: share, session: session, conn: conn}, nil
}

func (s *SMB) List(ctx context.Context, prefix string) ([]Entry, error) {
	share, session, conn, err := s.connect()
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
	share, session, conn, err := s.connect()
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
	share, session, conn, err := s.connect()
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
	conn, err := net.Dial("tcp", addr)
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
	session, err := d.Dial(conn)
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
	f       *smb2.File
	share   *smb2.Share
	session *smb2.Session
	conn    net.Conn
}

func (w *smbWriter) Write(p []byte) (int, error) { return w.f.Write(p) }
func (w *smbWriter) Close() error {
	w.f.Close()
	w.share.Umount()
	w.session.Logoff()
	return w.conn.Close()
}

type smbReader struct {
	f       *smb2.File
	share   *smb2.Share
	session *smb2.Session
	conn    net.Conn
}

func (r *smbReader) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r *smbReader) Close() error {
	r.f.Close()
	r.share.Umount()
	r.session.Logoff()
	return r.conn.Close()
}
