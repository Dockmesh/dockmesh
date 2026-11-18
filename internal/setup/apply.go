package setup

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/layout"
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

// DropRestoreFiles removes the backup and key file a wizard restore left
// in stateDir; the key opens every backup of the old install.
func DropRestoreFiles(stateDir string) error {
	var errs []error
	for _, name := range []string{RestoreArchiveFile, RestoreKeyFile} {
		if err := os.Remove(filepath.Join(stateDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ApplyValues are what a setup-apply commits. The service writes them,
// so root treats them as untrusted and checks them with Validate.
type ApplyValues struct {
	DataDir     string `json:"data_dir"`
	HTTPAddr    string `json:"http_addr"`
	TLSMode     string `json:"tls_mode"`
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
	BaseURL     string `json:"base_url"`
	// StacksDir is empty for DataDir/stacks. Outside the data dir root
	// checks it with the stacksdir rules and creates it when new.
	StacksDir string `json:"stacks_dir,omitempty"`
	// AgentListen is the agent listener, ":<port>"; empty is :8443.
	AgentListen string `json:"agent_listen,omitempty"`
	// AgentURL replaces the agent URL derived from BaseURL; it is saved
	// as a setting, so it stays editable in the app.
	AgentURL string `json:"agent_url,omitempty"`
	// TrustedProxies replaces the default trusted proxy of a listener
	// behind the operator's own proxy.
	TrustedProxies string `json:"trusted_proxies,omitempty"`
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
	if err := v.validateStacksDir(); err != nil {
		return err
	}
	if err := v.validateAgent(port); err != nil {
		return err
	}
	return v.validateTrustedProxies(mode)
}

// StacksRoot is the stacks dir v sets up.
func (v *ApplyValues) StacksRoot() string {
	if v.StacksDir == "" {
		return filepath.Join(v.DataDir, "stacks")
	}
	return v.StacksDir
}

// StacksOutsideDataDir reports a stacks dir that root creates apart from
// the data dir.
func (v *ApplyValues) StacksOutsideDataDir() bool {
	return v.StacksDir != ""
}

func (v *ApplyValues) validateStacksDir() error {
	p := v.StacksDir
	if p == "" || p == filepath.Join(v.DataDir, "stacks") {
		v.StacksDir = ""
		return nil
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || !plainValueRe.MatchString(p) || p == "/" {
		return fmt.Errorf("stacks dir %q must be a clean absolute path of letters, digits and ._-/", p)
	}
	if layout.Within(v.DataDir, p) || layout.Within(p, v.DataDir) {
		return fmt.Errorf("stacks dir %s overlaps the data dir %s; only %s may lie in it", p, v.DataDir, filepath.Join(v.DataDir, "stacks"))
	}
	if err := layout.CheckWritablePath(p, v.DataDir); err != nil {
		return fmt.Errorf("stacks dir: %w", err)
	}
	return nil
}

func (v *ApplyValues) validateAgent(webPort string) error {
	if v.AgentListen == "" {
		v.AgentListen = AgentListen
	}
	host, port, err := net.SplitHostPort(v.AgentListen)
	if err != nil || host != "" || !validPort(port) {
		return fmt.Errorf("agent listener %q must be :<port> with a port from 1 to 65535", v.AgentListen)
	}
	if port == webPort {
		return errors.New("web port and agent port must differ")
	}
	if v.AgentURL != "" {
		if len(v.AgentURL) > 2048 || strings.ContainsAny(v.AgentURL, " \t\r\n") {
			return fmt.Errorf("agent URL %q is not a URL", v.AgentURL)
		}
		if err := agents.ValidateAgentURL(v.AgentURL); err != nil {
			return fmt.Errorf("agent URL %w", err)
		}
	}
	return nil
}

// maxTrustedProxies bounds the trusted proxy list.
const maxTrustedProxies = 32

func (v *ApplyValues) validateTrustedProxies(mode webtls.Mode) error {
	if v.TrustedProxies == "" {
		return nil
	}
	if mode != webtls.Proxy {
		return errors.New("trusted proxies are only set with the own reverse proxy option")
	}
	nets, err := clientip.ParseList(v.TrustedProxies)
	if err != nil {
		return fmt.Errorf("trusted proxies: %w", err)
	}
	if len(nets) == 0 || len(nets) > maxTrustedProxies {
		return fmt.Errorf("trusted proxies: give 1 to %d addresses or networks", maxTrustedProxies)
	}
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		if n.Bits() == 0 {
			return fmt.Errorf("trusted proxies: %s would trust every address", n)
		}
		out = append(out, n.String())
	}
	v.TrustedProxies = strings.Join(out, ",")
	return nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == p
}


// ConfigEnv is the server config a setup-apply writes, for v (validated)
// and the asset dir install.sh uses for the agent binaries.
func (v *ApplyValues) ConfigEnv(assetDir string) ([]byte, error) {
	env := map[string]string{
		"DOCKMESH_DATA_DIR":       v.DataDir,
		"DOCKMESH_HTTP_ADDR":      v.HTTPAddr,
		"DOCKMESH_AGENT_LISTEN":   v.AgentListen,
		"DOCKMESH_TLS_MODE":       v.TLSMode,
		"DOCKMESH_INSTALL_SCRIPT": filepath.Join(assetDir, "install-agent.sh"),
		"DOCKMESH_BINARY_DIR":     filepath.Join(assetDir, "bin"),
	}
	mode := webtls.Mode(v.TLSMode)
	if mode == webtls.File {
		env["DOCKMESH_TLS_CERT_FILE"] = v.TLSCertFile
		env["DOCKMESH_TLS_KEY_FILE"] = v.TLSKeyFile
	}
	if v.StacksDir != "" {
		env["DOCKMESH_STACKS_ROOT"] = v.StacksDir
	}
	tp := v.TrustedProxies
	if tp == "" {
		tp = webtls.DefaultTrustedProxies(mode, v.HTTPAddr)
	}
	if tp != "" {
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
