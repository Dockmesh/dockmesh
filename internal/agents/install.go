package agents

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

// InstallScriptPath and InstallBinaryPrefix are the unauthenticated routes
// on the agent listener that hand out the installer and the agent binary.
// The install command pins the listener key, so both arrive over a channel
// the new host can verify before running anything as root.
const (
	InstallScriptPath   = "/install/agent.sh"
	InstallBinaryPrefix = "/install/"
)

var installTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ValidInstallToken reports whether token has the shape newToken produces.
// The token is pasted into a shell script, so nothing else may get through.
func ValidInstallToken(token string) bool { return installTokenRe.MatchString(token) }

// installURL is the https origin of the agent listener as agent hosts
// reach it, taken from the public agent URL.
func (s *Service) installURL() string {
	return installURLFromAgentURL(s.AgentURL())
}

func installURLFromAgentURL(agentURL string) string {
	u, err := url.Parse(agentURL)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && u.Scheme != "https") {
		return ""
	}
	return "https://" + u.Host
}

// installPin is the listener key in curl's --pinnedpubkey form.
func (s *Service) installPin() string {
	if s.pki == nil {
		return ""
	}
	return s.pki.ServerKeyPin()
}

// InstallCommand is the one-liner for a new host. The token is an
// installer argument so it never appears in a URL or an access log.
func (s *Service) InstallCommand(token string) string {
	return fmt.Sprintf("curl -fsSLk --pinnedpubkey '%s' '%s%s' | sudo bash -s -- --token %s",
		s.installPin(), s.installURL(), InstallScriptPath, token)
}

// installScriptTemplate is where the server reads install-agent.sh from.
func installScriptTemplate() string {
	if p := os.Getenv("DOCKMESH_INSTALL_SCRIPT"); p != "" {
		return p
	}
	return "./scripts/install-agent.sh"
}

// RenderInstallScript fills the installer template with this server's
// addresses, listener pin, agents CA and binary checksums. token is empty
// for the generic script on the agent listener and set by the legacy
// web-port route.
func (s *Service) RenderInstallScript(token string) ([]byte, error) {
	if token != "" && !ValidInstallToken(token) {
		return nil, errors.New("malformed token")
	}
	enrollURL, installURL, pin := s.EnrollURL(), s.installURL(), s.installPin()
	if enrollURL == "" || installURL == "" || pin == "" {
		return nil, errors.New("agent listener URL is not configured (DOCKMESH_AGENT_PUBLIC_URL)")
	}
	raw, err := os.ReadFile(installScriptTemplate())
	if err != nil {
		return nil, errors.New("installer script not found")
	}
	// A missing binary leaves its checksum empty; the script then refuses
	// to install on that architecture.
	sumAMD64, _ := AgentBinarySHA256("amd64")
	sumARM64, _ := AgentBinarySHA256("arm64")
	out := strings.NewReplacer(
		"{{TOKEN}}", token,
		"{{INSTALL_URL}}", installURL,
		"{{INSTALL_PIN}}", pin,
		"{{ENROLL_URL}}", enrollURL,
		"{{AGENT_URL}}", s.AgentURL(),
		"{{CA_FINGERPRINT}}", s.CAFingerprint(),
		"{{CA_CERT_PEM}}", strings.TrimSpace(string(s.CACertPEM())),
		"{{BINARY_SHA256_AMD64}}", sumAMD64,
		"{{BINARY_SHA256_ARM64}}", sumARM64,
	).Replace(string(raw))
	return []byte(out), nil
}

// InstallHandler serves the generic installer and the agent binaries on
// the agent listener. Every request counts against the per-IP limiter:
// the routes are public and a binary download is the most expensive thing
// the listener does for an unauthenticated client.
func InstallHandler(svc *Service, limiter *ratelimit.Limiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if limiter != nil {
			if _, ok, retry := limiter.Acquire(peerLimitKey(r)); !ok {
				writeTooMany(w, retry)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == InstallScriptPath {
			script, err := svc.RenderInstallScript("")
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
			_, _ = w.Write(script)
			return
		}
		serveAgentBinary(w, r, strings.TrimPrefix(r.URL.Path, InstallBinaryPrefix))
	})
}

// serveAgentBinary writes the bundled agent binary named requested. The
// file name comes from AgentBinaryName, never from the request, so only
// the bundled agent binaries can be served.
func serveAgentBinary(w http.ResponseWriter, r *http.Request, requested string) {
	var name string
	for _, arch := range []string{"amd64", "arm64"} {
		if n, _ := AgentBinaryName(arch); n == requested {
			name = n
		}
	}
	if name == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(BinaryDir(), name))
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, ErrNoAgentBinary.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "open binary", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "stat binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// peerLimitKey is the limiter key for the TCP peer. The agent listener
// terminates TLS itself, so forwarding headers are never trusted here.
func peerLimitKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return clientip.LimitKey(host)
}

func writeTooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}
