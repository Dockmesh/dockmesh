package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// maxSetupFile bounds a setup file; a real one is well under 2 KiB.
const maxSetupFile = 64 << 10

// File is a setup file for `dockmesh setup apply --file`: the wizard's
// answers for a new install (Admin) or a restore (Restore). Secrets are
// only ever named by a file, or given as an argon2id hash.
type File struct {
	DataDir string       `yaml:"data_dir"`
	Admin   *FileAdmin   `yaml:"admin"`
	Address FileAddress  `yaml:"address"`
	TLS     FileTLS      `yaml:"tls"`
	Restore *FileRestore `yaml:"restore"`
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
}

// FileTLS picks the certificate option.
type FileTLS struct {
	Mode     string `yaml:"mode"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
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

// SecretFiles are the password, the backup and its key.
func (f File) SecretFiles() []SecretFile {
	var out []SecretFile
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
