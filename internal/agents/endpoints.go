package agents

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/dockmesh/dockmesh/internal/pki"
)

// Where an effective URL came from.
const (
	SourceEnv     = "env"
	SourceSetting = "setting"
	SourceDerived = "derived"
	SourceDefault = "default"
)

// DefaultAgentPort is the agent listener port used when the listen
// address does not name one.
const DefaultAgentPort = "8443"

// Endpoints are the URLs handed to agents: BaseURL serves the installer
// and agent binaries, AgentURL is the wss:// address of the mTLS listener.
type Endpoints struct {
	BaseURL     string `json:"base_url"`
	BaseSource  string `json:"base_source"`
	AgentURL    string `json:"agent_url"`
	AgentSource string `json:"agent_source"`
}

// EndpointInputs feeds ResolveEndpoints. Env values win over settings so
// an install configured through dockmesh.env keeps its addresses.
type EndpointInputs struct {
	EnvBaseURL      string
	EnvAgentURL     string
	SettingBaseURL  string
	SettingAgentURL string
	DefaultBaseURL  string
	AgentListen     string
}

// ResolveEndpoints picks each URL from env, then the stored setting, then
// a default; an unset agent URL is derived from the base URL. Both URLs end
// up in a script that agent hosts run as root, so a value that fails
// validation (one stored before validation existed, or a bad env value) is
// skipped with a warning and the next source is used.
func ResolveEndpoints(in EndpointInputs) Endpoints {
	var e Endpoints
	e.BaseURL, e.BaseSource = pickURL(ValidateBaseURL, "base URL",
		[2]string{in.EnvBaseURL, SourceEnv}, [2]string{in.SettingBaseURL, SourceSetting})
	if e.BaseURL == "" {
		e.BaseURL, e.BaseSource = in.DefaultBaseURL, SourceDefault
	}
	e.BaseURL = strings.TrimRight(e.BaseURL, "/")
	e.AgentURL, e.AgentSource = pickURL(ValidateAgentURL, "agent URL",
		[2]string{in.EnvAgentURL, SourceEnv}, [2]string{in.SettingAgentURL, SourceSetting})
	if e.AgentURL == "" {
		e.AgentURL, e.AgentSource = DeriveAgentURL(e.BaseURL, in.AgentListen), SourceDerived
	}
	return e
}

func pickURL(validate func(string) error, what string, candidates ...[2]string) (string, string) {
	for _, c := range candidates {
		v := strings.TrimSpace(c[0])
		if v == "" {
			continue
		}
		if err := validate(v); err != nil {
			slog.Warn("ignoring invalid "+what, "source", c[1], "value", v, "err", err)
			continue
		}
		return v, c[1]
	}
	return "", ""
}

// DeriveAgentURL returns wss://<host of baseURL>:<listener port>/connect.
func DeriveAgentURL(baseURL, listen string) string {
	host := "localhost"
	if u, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return "wss://" + net.JoinHostPort(host, listenPort(listen)) + "/connect"
}

func listenPort(listen string) string {
	if _, port, err := net.SplitHostPort(strings.TrimSpace(listen)); err == nil && port != "" {
		return port
	}
	return DefaultAgentPort
}

// ValidateBaseURL accepts an absolute http(s) URL with no query or
// fragment whose host can go into the listener certificate, since the
// agent URL is derived from it.
func ValidateBaseURL(raw string) error {
	if err := validateURL(raw, "http", "https"); err != nil {
		return err
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	return pki.ValidateSAN(u.Hostname())
}

// ValidateAgentURL accepts an absolute wss:// URL whose host can go into
// the listener certificate.
func ValidateAgentURL(raw string) error {
	if err := validateURL(raw, "wss"); err != nil {
		return err
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	return pki.ValidateSAN(u.Hostname())
}

func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if !slices.Contains(schemes, u.Scheme) {
		return fmt.Errorf("must start with %s://", strings.Join(schemes, ":// or "))
	}
	if u.Hostname() == "" {
		return errors.New("has no host")
	}
	if strings.Contains(u.Hostname(), "*") {
		return errors.New("must name one host, not a wildcard")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not contain credentials, a query or a fragment")
	}
	if strings.ContainsAny(raw, "'\"`$\\ \t\r\n") {
		return errors.New("contains spaces, quotes or shell characters")
	}
	return nil
}

// SetEndpointResolver installs the function ReloadEndpoints reads the
// current configuration from.
func (s *Service) SetEndpointResolver(f func() Endpoints) {
	s.urlMu.Lock()
	s.resolve = f
	s.urlMu.Unlock()
}

// SetListenerEnabled records whether the mTLS agent listener runs, for
// CheckReachability.
func (s *Service) SetListenerEnabled(on bool) {
	s.urlMu.Lock()
	s.listenerOn = on
	s.urlMu.Unlock()
}

// Endpoints returns the URLs in use and where they came from.
func (s *Service) Endpoints() Endpoints {
	s.urlMu.RLock()
	defer s.urlMu.RUnlock()
	return s.endpoints
}

// UseEndpoints switches install commands, enrollment and upgrades to e
// without touching the listener cert.
func (s *Service) UseEndpoints(e Endpoints) {
	s.urlMu.Lock()
	s.endpoints = e
	s.urlMu.Unlock()
}

// ReloadEndpoints re-reads the configuration, switches install commands,
// enrollment and upgrades to it and makes the listener cert cover the
// agent URL's host. The cert keeps its old names and IPs, so agents
// enrolled under an earlier address keep connecting.
func (s *Service) ReloadEndpoints() (Endpoints, error) {
	s.urlMu.RLock()
	f := s.resolve
	s.urlMu.RUnlock()
	if f == nil {
		return s.Endpoints(), nil
	}
	e := f()
	s.UseEndpoints(e)

	host := hostOf(e.AgentURL)
	if host == "" || s.pki == nil {
		return e, nil
	}
	changed, err := s.pki.EnsureServerHosts([]string{host})
	if err != nil {
		return e, fmt.Errorf("add %s to the agent server certificate: %w", host, err)
	}
	if changed {
		slog.Info("agent server certificate re-issued for the agent URL", "host", host, "sans", s.pki.ServerCertSANs())
	}
	return e, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
