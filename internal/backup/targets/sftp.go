package targets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	// HostKey is the server key accepted on first contact, in
	// authorized_keys format. HostKeyPending holds a different key the
	// server presented later, until an admin accepts it.
	HostKey        string `json:"host_key,omitempty"`
	HostKeyPending string `json:"host_key_pending,omitempty"`
}

// HostKeyEvent reports what the server's host key looked like on connect.
type HostKeyEvent struct {
	Kind           string // "recorded" (first contact), "matched" or "changed"
	Key            string
	Fingerprint    string
	OldFingerprint string
}

// HostKeyChangedError fails a connection whose host key differs from the
// recorded one.
type HostKeyChangedError struct{ Old, New string }

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("sftp: host key changed (recorded %s, server now presents %s); accept the new key only if you know why it changed", e.Old, e.New)
}

type SFTP struct {
	cfg       SFTPConfig
	onHostKey func(HostKeyEvent) error
}

// OnHostKey installs a callback for host key events (trust on first use).
// An error from it refuses the connection.
func (s *SFTP) OnHostKey(f func(HostKeyEvent) error) { s.onHostKey = f }

// checkHostKey trusts the first key it sees and afterwards only that key.
func (s *SFTP) checkHostKey(_ string, _ net.Addr, key ssh.PublicKey) error {
	got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	fp := ssh.FingerprintSHA256(key)
	ev := HostKeyEvent{Key: got, Fingerprint: fp}
	switch {
	case s.cfg.HostKey == "":
		ev.Kind = "recorded"
		s.cfg.HostKey = got
	case s.cfg.HostKey == got:
		ev.Kind = "matched"
	default:
		ev.Kind = "changed"
		ev.OldFingerprint = s.cfg.HostKey
		if old, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.cfg.HostKey)); err == nil {
			ev.OldFingerprint = ssh.FingerprintSHA256(old)
		}
	}
	if s.onHostKey != nil {
		if err := s.onHostKey(ev); err != nil {
			return err
		}
	}
	if ev.Kind == "changed" {
		return &HostKeyChangedError{Old: ev.OldFingerprint, New: fp}
	}
	return nil
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
		HostKeyCallback: s.checkHostKey,
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	conn, err := dialSSH(addr, sshCfg)
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
	// Runs create the base path on first write, so the test does too;
	// otherwise it would fail for a directory the run would have made.
	if err := client.MkdirAll(s.cfg.Path); err != nil {
		return 0, 0, fmt.Errorf("sftp: create %s: %w", s.cfg.Path, err)
	}
	stat, err := client.StatVFS(s.cfg.Path)
	if err != nil {
		// statvfs is an OpenSSH extension; the path is usable without it.
		return 0, 0, nil
	}
	total = int64(stat.Blocks * stat.Frsize)
	free := int64(stat.Bavail * stat.Frsize)
	used = total - free
	return total, used, nil
}

// sshHandshakeTimeout bounds the SSH handshake; ssh.ClientConfig.Timeout
// only covers the TCP connect, so a server that accepts the connection and
// then stops answering would hold the dial forever.
const sshHandshakeTimeout = 30 * time.Second

// dialSSH connects over an idle-deadline connection, so a transfer that
// stops moving fails instead of hanging.
func dialSSH(addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	raw, err := dialTCP(addr)
	if err != nil {
		return nil, err
	}
	stuck := time.AfterFunc(sshHandshakeTimeout, func() { _ = raw.Close() })
	c, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if !stuck.Stop() && err == nil {
		c.Close()
		err = fmt.Errorf("ssh handshake with %s timed out", addr)
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

type sftpWriter struct {
	f      *sftp.File
	client *sftp.Client
	conn   *ssh.Client
}

func (w *sftpWriter) Write(p []byte) (int, error) { return w.f.Write(p) }
func (w *sftpWriter) Break() error                { return w.conn.Close() }
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
func (r *sftpReader) Break() error                { return r.conn.Close() }
func (r *sftpReader) Close() error {
	r.f.Close()
	r.client.Close()
	return r.conn.Close()
}

func init() {
	// Keep the json import from being unused.
	_ = json.Marshal
}
