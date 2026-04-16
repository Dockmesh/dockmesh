package targets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Path       string `json:"path"`
}

type SFTP struct {
	cfg SFTPConfig
}

func NewSFTP(raw any) (*SFTP, error) {
	var cfg SFTPConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("sftp: host required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Path == "" {
		cfg.Path = "/backups"
	}
	return &SFTP{cfg: cfg}, nil
}

func (s *SFTP) connect() (*sftp.Client, *ssh.Client, error) {
	var authMethods []ssh.AuthMethod
	if s.cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(s.cfg.PrivateKey))
		if err != nil {
			return nil, nil, fmt.Errorf("sftp: parse key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if s.cfg.Password != "" {
		authMethods = append(authMethods, ssh.Password(s.cfg.Password))
	}

	sshCfg := &ssh.ClientConfig{
		User:            s.cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: ssh dial: %w", err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sftp: client: %w", err)
	}
	return client, conn, nil
}

func (s *SFTP) Open(ctx context.Context, path string) (io.WriteCloser, error) {
	client, conn, err := s.connect()
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	if err := client.MkdirAll(filepath.Dir(full)); err != nil {
		client.Close()
		conn.Close()
		return nil, fmt.Errorf("sftp: mkdir: %w", err)
	}
	f, err := client.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		client.Close()
		conn.Close()
		return nil, fmt.Errorf("sftp: open: %w", err)
	}
	return &sftpWriter{f: f, client: client, conn: conn}, nil
}

func (s *SFTP) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	client, conn, err := s.connect()
	if err != nil {
		return nil, err
	}
	full := filepath.Join(s.cfg.Path, filepath.FromSlash(path))
	f, err := client.Open(full)
	if err != nil {
		client.Close()
		conn.Close()
		return nil, err
	}
	return &sftpReader{f: f, client: client, conn: conn}, nil
}

func (s *SFTP) List(ctx context.Context, prefix string) ([]Entry, error) {
	client, conn, err := s.connect()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	defer conn.Close()

	root := filepath.Join(s.cfg.Path, filepath.FromSlash(prefix))
	walker := client.Walk(root)
	var out []Entry
	for walker.Step() {
		if walker.Err() != nil || walker.Stat().IsDir() {
			continue
		}
		rel, _ := filepath.Rel(s.cfg.Path, walker.Path())
		out = append(out, Entry{
			Path:    filepath.ToSlash(rel),
			Size:    walker.Stat().Size(),
			ModTime: walker.Stat().ModTime(),
		})
	}
	return out, nil
}

func (s *SFTP) Delete(ctx context.Context, path string) error {
	client, conn, err := s.connect()
	if err != nil {
		return err
	}
	defer client.Close()
	defer conn.Close()
	return client.Remove(filepath.Join(s.cfg.Path, filepath.FromSlash(path)))
}

// StorageInfo returns total/used bytes via statvfs.
func (s *SFTP) StorageInfo() (total, used int64, err error) {
	client, conn, err := s.connect()
	if err != nil {
		return 0, 0, err
	}
	defer client.Close()
	defer conn.Close()
	stat, err := client.StatVFS(s.cfg.Path)
	if err != nil {
		return 0, 0, err
	}
	total = int64(stat.Blocks * stat.Frsize)
	free := int64(stat.Bavail * stat.Frsize)
	used = total - free
	return total, used, nil
}

type sftpWriter struct {
	f      *sftp.File
	client *sftp.Client
	conn   *ssh.Client
}

func (w *sftpWriter) Write(p []byte) (int, error) { return w.f.Write(p) }
func (w *sftpWriter) Close() error {
	w.f.Close()
	w.client.Close()
	return w.conn.Close()
}

type sftpReader struct {
	f      *sftp.File
	client *sftp.Client
	conn   *ssh.Client
}

func (r *sftpReader) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r *sftpReader) Close() error {
	r.f.Close()
	r.client.Close()
	return r.conn.Close()
}

func init() {
	// Keep the json import from being unused.
	_ = json.Marshal
}
