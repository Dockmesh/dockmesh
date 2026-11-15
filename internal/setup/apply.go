package setup

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/webtls"
)

// AgentListen is the agent listener of a new install.
const AgentListen = ":8443"

// ApplyFile is the file in StateDir where the service leaves what the
// wizard entered for root's setup-apply. Passwords are only in it as
// hashes.
const ApplyFile = "apply.json"

// RestoreArchiveFile and RestoreKeyFile are the uploaded system backup and
// its key file in StateDir for a restore through setup-apply.
const (
	RestoreArchiveFile = "restore.archive"
	RestoreKeyFile     = "restore.key"
)

// ApplyValues are what a setup-apply commits. The service writes them,
// so root treats them as untrusted and checks them with Validate.
type ApplyValues struct {
	DataDir     string `json:"data_dir"`
	HTTPAddr    string `json:"http_addr"`
	TLSMode     string `json:"tls_mode"`
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
	BaseURL     string `json:"base_url"`
	// Restore restores the system backup the service left as
	// RestoreArchiveFile (and RestoreKeyFile) in the state dir instead of
	// creating an admin.
	Restore bool `json:"restore,omitempty"`
	Admin   struct {
		Username     string `json:"username"`
		Email        string `json:"email,omitempty"`
		PasswordHash string `json:"password_hash"`
	} `json:"admin"`
}

var (
	adminNameRe = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)
	// plainValueRe is what may go into the config unquoted: systemd and
	// the server's own parser read it the same way.
	plainValueRe = regexp.MustCompile(`^[A-Za-z0-9._/:@%+=,\[\]-]*$`)
)

// systemDirs may never become a data dir, nor contain one.
var systemDirs = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var/lib/docker"}

// systemParents may contain a data dir but not be one.
var systemParents = []string{"/", "/home", "/media", "/mnt", "/opt", "/srv", "/tmp", "/var", "/var/lib", "/usr/local"}

// CheckDataDirPath refuses a data dir that is not a clean absolute path,
// is a system directory or lies below one, or below Docker's own data.
func CheckDataDirPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || !plainValueRe.MatchString(p) {
		return fmt.Errorf("data dir %q must be a clean absolute path of letters, digits and ._-/", p)
	}
	for _, s := range systemDirs {
		if p == s || strings.HasPrefix(p, s+"/") {
			return fmt.Errorf("data dir %s is inside the system directory %s", p, s)
		}
	}
	for _, s := range systemParents {
		if p == s {
			return fmt.Errorf("data dir %s is a system directory", p)
		}
	}
	return nil
}

// Validate checks every value and fills the defaults: the internal
// certificate, port 8080, and loopback for a listener behind a proxy.
func (v *ApplyValues) Validate() error {
	if err := v.ValidateServer(); err != nil {
		return err
	}
	if v.Restore {
		if v.Admin.Username != "" || v.Admin.PasswordHash != "" {
			return errors.New("a restore brings its accounts from the backup; it takes no admin")
		}
		return nil
	}
	if !adminNameRe.MatchString(v.Admin.Username) {
		return errors.New("admin username must be 3-32 characters of a-z, 0-9, . _ -")
	}
	if strings.ContainsAny(v.Admin.Email, "\r\n\x00") || len(v.Admin.Email) > 254 {
		return errors.New("admin email is not valid")
	}
	if !strings.HasPrefix(v.Admin.PasswordHash, "$argon2id$") || strings.ContainsAny(v.Admin.PasswordHash, "\r\n\x00 ") {
		return errors.New("admin password hash is not an argon2id hash")
	}
	return nil
}

// ValidateServer is Validate without the admin account, which a restore
// brings from its backup.
func (v *ApplyValues) ValidateServer() error {
	if err := CheckDataDirPath(v.DataDir); err != nil {
		return err
	}
	mode, err := webtls.ParseMode(v.TLSMode)
	if err != nil {
		return err
	}
	if mode == webtls.Plain {
		mode = webtls.Internal
	}
	v.TLSMode = string(mode)
	if v.HTTPAddr == "" {
		v.HTTPAddr = webtls.ListenAddr(mode, "")
	}
	host, port, err := net.SplitHostPort(v.HTTPAddr)
	if err != nil || !plainValueRe.MatchString(v.HTTPAddr) {
		return fmt.Errorf("listen address %q is not host:port", v.HTTPAddr)
	}
	if host != "" && net.ParseIP(host) == nil {
		return fmt.Errorf("listen address %q needs an IP address, not a name", v.HTTPAddr)
	}
	if _, err := net.LookupPort("tcp", port); err != nil || strings.Trim(port, "0123456789") != "" {
		return fmt.Errorf("listen port %q is not a number", port)
	}
	if mode == webtls.File {
		for _, f := range []string{v.TLSCertFile, v.TLSKeyFile} {
			if !filepath.IsAbs(f) || filepath.Clean(f) != f || !plainValueRe.MatchString(f) {
				return fmt.Errorf("certificate file %q must be a clean absolute path", f)
			}
		}
	} else if v.TLSCertFile != "" || v.TLSKeyFile != "" {
		return errors.New("certificate files are only used with the own-certificate option")
	}
	if v.BaseURL != "" {
		u, err := url.Parse(v.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(v.BaseURL, " \t\r\n") {
			return fmt.Errorf("base URL %q is not an http(s) URL", v.BaseURL)
		}
	}
	return nil
}

// ConfigEnv is the server config a setup-apply writes, for v (validated)
// and the asset dir install.sh uses for the agent binaries.
func (v *ApplyValues) ConfigEnv(assetDir string) ([]byte, error) {
	env := map[string]string{
		"DOCKMESH_DATA_DIR":       v.DataDir,
		"DOCKMESH_HTTP_ADDR":      v.HTTPAddr,
		"DOCKMESH_AGENT_LISTEN":   AgentListen,
		"DOCKMESH_TLS_MODE":       v.TLSMode,
		"DOCKMESH_INSTALL_SCRIPT": filepath.Join(assetDir, "install-agent.sh"),
		"DOCKMESH_BINARY_DIR":     filepath.Join(assetDir, "bin"),
	}
	mode := webtls.Mode(v.TLSMode)
	if mode == webtls.File {
		env["DOCKMESH_TLS_CERT_FILE"] = v.TLSCertFile
		env["DOCKMESH_TLS_KEY_FILE"] = v.TLSKeyFile
	}
	if tp := webtls.DefaultTrustedProxies(mode, v.HTTPAddr); tp != "" {
		env["DOCKMESH_TRUSTED_PROXIES"] = tp
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Written by dockmesh setup. Changes take effect after: sudo systemctl restart dockmesh\n")
	for _, k := range keys {
		if !plainValueRe.MatchString(env[k]) {
			return nil, fmt.Errorf("%s: value %q not allowed in the config", k, env[k])
		}
		b.WriteString(k + "=" + env[k] + "\n")
	}
	return []byte(b.String()), nil
}

// Fresh reports facts of an install without config, database, data or
// any other database: setup runs on the state dir until it is applied.
func Fresh(f Facts) bool {
	return !f.Config && !f.DB && !f.Data && !f.ForeignDB
}
