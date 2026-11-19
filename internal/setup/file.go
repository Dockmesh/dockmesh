package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dockmesh/dockmesh/internal/notify"
)

// maxSetupFile bounds a setup file; a real one is well under 2 KiB.
const maxSetupFile = 64 << 10

// File is a setup file for `dockmesh setup apply --file`: the wizard's
// answers for a new install (Admin) or a restore (Restore). Secrets are
// only ever named by a file, or given as an argon2id hash.
type File struct {
	DataDir string `yaml:"data_dir"`
	// StacksDir is empty for <data_dir>/stacks.
	StacksDir string      `yaml:"stacks_dir"`
	Admin     *FileAdmin  `yaml:"admin"`
	Address   FileAddress `yaml:"address"`
	TLS       FileTLS     `yaml:"tls"`
	Proxy     FileProxy   `yaml:"proxy"`
	// Notifications are channels to create: type, name and the channel's
	// fields; a secret field only as <field>_file.
	Notifications []map[string]any `yaml:"notifications"`
	Restore       *FileRestore     `yaml:"restore"`
}

// FileAdmin is the admin account of a new install.
type FileAdmin struct {
	Username     string `yaml:"username"`
	Email        string `yaml:"email"`
	PasswordFile string `yaml:"password_file"`
	PasswordHash string `yaml:"password_hash"`
}

// FileAddress is where the server listens and is reached.
type FileAddress struct {
	BaseURL  string `yaml:"base_url"`
	HTTPAddr string `yaml:"http_addr"`
	// AgentPort is the agent listener's port; 0 is 8443.
	AgentPort int `yaml:"agent_port"`
	// AgentURL replaces the agent URL derived from BaseURL.
	AgentURL string `yaml:"agent_url"`
}

// FileTLS picks the certificate option.
type FileTLS struct {
	Mode     string `yaml:"mode"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// TrustedProxies replaces the default with mode proxy: addresses or
	// networks, comma separated.
	TrustedProxies string `yaml:"trusted_proxies"`
}

// FileProxy is the reverse proxy step: the managed Caddy proxy. TLS mode
// caddy turns it on.
type FileProxy struct {
	Enabled   bool   `yaml:"enabled"`
	ACMEEmail string `yaml:"acme_email"`
}

// FileRestore names the system backup a restore brings back.
type FileRestore struct {
	BackupFile string `yaml:"backup_file"`
	KeyFile    string `yaml:"key_file"`
}

// ParseFile reads a setup file. Unknown keys are refused, so a typo or a
// plaintext `password:` never passes unnoticed.
func ParseFile(r io.Reader) (File, error) {
	var f File
	data, err := io.ReadAll(io.LimitReader(r, maxSetupFile+1))
	if err != nil {
		return f, err
	}
	if len(data) > maxSetupFile {
		return f, errors.New("setup file too large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("setup file: %w", err)
	}
	return f, f.check()
}

func (f File) check() error {
	switch {
	case f.Admin == nil && f.Restore == nil:
		return errors.New("setup file: needs either admin (new install) or restore")
	case f.Admin != nil && f.Restore != nil:
		return errors.New("setup file: admin and restore exclude each other; a restore brings its own accounts")
	}
	if f.Admin != nil {
		a := f.Admin
		if (a.PasswordFile == "") == (a.PasswordHash == "") {
			return errors.New("setup file: admin needs exactly one of password_file and password_hash")
		}
		if a.PasswordFile != "" && !filepath.IsAbs(a.PasswordFile) {
			return fmt.Errorf("setup file: admin.password_file %q must be an absolute path", a.PasswordFile)
		}
	}
	if p := f.Address.AgentPort; p < 0 || p > 65535 {
		return fmt.Errorf("setup file: address.agent_port %d is not a port from 1 to 65535", p)
	}
	if f.Restore != nil && len(f.Notifications) > 0 {
		return errors.New("setup file: notifications come from the backup in a restore")
	}
	for i, n := range f.Notifications {
		if _, err := FileNotification(n).channelFields(i); err != nil {
			return err
		}
	}
	if r := f.Restore; r != nil {
		if !filepath.IsAbs(r.BackupFile) {
			return fmt.Errorf("setup file: restore.backup_file %q must be an absolute path", r.BackupFile)
		}
		if r.KeyFile != "" && !filepath.IsAbs(r.KeyFile) {
			return fmt.Errorf("setup file: restore.key_file %q must be an absolute path", r.KeyFile)
		}
	}
	return nil
}

// FileNotification is one entry of a setup file's notifications.
type FileNotification map[string]any

// NotificationFields is a setup file channel split into what the file
// gives inline and the files that hold its secrets.
type NotificationFields struct {
	Type, Name string
	Config     map[string]any
	Secrets    []NotificationSecret
}

// NotificationSecret is the file that holds the secret config Field.
type NotificationSecret struct {
	SecretFile
	Field string
}

// channelFields checks entry i and splits it. Secret fields given inline
// are refused, like a plain admin password.
func (n FileNotification) channelFields(i int) (NotificationFields, error) {
	out := NotificationFields{Config: map[string]any{}}
	prefix := fmt.Sprintf("setup file: notifications[%d]", i)
	t, _ := n["type"].(string)
	if t == "" {
		return out, fmt.Errorf("%s: needs a type (%s)", prefix, notify.Types)
	}
	if !notify.ValidType(t) {
		return out, fmt.Errorf("%s: unknown type %q (%s)", prefix, t, notify.Types)
	}
	fields := notify.ConfigFields(t)
	out.Type = t
	if v, ok := n["name"]; ok {
		name, ok := v.(string)
		if !ok {
			return out, fmt.Errorf("%s.name must be a string", prefix)
		}
		out.Name = name
	}
	for k, v := range n {
		if k == "type" || k == "name" {
			continue
		}
		if base, ok := strings.CutSuffix(k, "_file"); ok && slices.Contains(fields, base) && !notify.PublicConfigField(t, base) {
			p, ok := v.(string)
			if !ok || !filepath.IsAbs(p) {
				return out, fmt.Errorf("%s.%s must be an absolute path", prefix, k)
			}
			out.Secrets = append(out.Secrets, NotificationSecret{SecretFile{fmt.Sprintf("notifications[%d].%s", i, k), p}, base})
			continue
		}
		if !slices.Contains(fields, k) {
			return out, fmt.Errorf("%s: %s is not a field of a %s channel", prefix, k, t)
		}
		if !notify.PublicConfigField(t, k) {
			return out, fmt.Errorf("%s: %s is a secret; put it alone in a file and name it with %s_file", prefix, k, k)
		}
		out.Config[k] = v
	}
	return out, nil
}

// NotificationFields returns entry i of f's notifications split up.
func (f File) NotificationFields(i int) (NotificationFields, error) {
	return FileNotification(f.Notifications[i]).channelFields(i)
}

// DefaultFileDataDir is the data dir of a setup file that names none.
const DefaultFileDataDir = "/var/lib/dockmesh"

// DataDirOrDefault is the data dir the file asks for.
func (f File) DataDirOrDefault() string {
	if f.DataDir == "" {
		return DefaultFileDataDir
	}
	return f.DataDir
}

// SecretFile is a file a setup file names that holds a secret, or data
// as private as one, with the key that names it.
type SecretFile struct {
	Key, Path string
}

// SecretFiles are the password, the backup and its key, and the secret
// files of notification channels.
func (f File) SecretFiles() []SecretFile {
	var out []SecretFile
	for i, n := range f.Notifications {
		fields, _ := FileNotification(n).channelFields(i)
		for _, sf := range fields.Secrets {
			out = append(out, sf.SecretFile)
		}
	}
	if f.Admin != nil && f.Admin.PasswordFile != "" {
		out = append(out, SecretFile{"admin.password_file", f.Admin.PasswordFile})
	}
	if f.Restore != nil {
		out = append(out, SecretFile{"restore.backup_file", f.Restore.BackupFile})
		if f.Restore.KeyFile != "" {
			out = append(out, SecretFile{"restore.key_file", f.Restore.KeyFile})
		}
	}
	return out
}

// ReadableWarning returns the warning for a file that group or others may
// read, empty when only its owner can.
func ReadableWarning(path string) string {
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm()&0o044 == 0 {
		return ""
	}
	return fmt.Sprintf("%s is readable by other users (mode %04o). Run: chmod 600 %s", path, fi.Mode().Perm(), path)
}

// ReadableByOthers reports a file that group or others may read.
func ReadableByOthers(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm()&0o044 != 0
}
