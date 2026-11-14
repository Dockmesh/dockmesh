package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// echo is what the test backends answer: the request as they saw it.
type echo struct {
	Backend string      `json:"backend"`
	URI     string      `json:"uri"`
	Host    string      `json:"host"`
	Header  http.Header `json:"header"`
}

func echoHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "backend")
		_ = json.NewEncoder(w).Encode(echo{Backend: name, URI: r.RequestURI, Host: r.Host, Header: r.Header})
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startCaddy runs the binary named by DOCKMESH_TEST_CADDY (skipped when
// unset) with its admin API on a local port and returns that address.
func startCaddy(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("DOCKMESH_TEST_CADDY")
	if bin == "" {
		t.Skip("DOCKMESH_TEST_CADDY is not set")
	}
	dir := t.TempDir()
	admin := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	boot := filepath.Join(dir, "boot.json")
	if err := os.WriteFile(boot, []byte(`{"admin":{"listen":"`+admin+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "run", "--config", boot) //nolint:gosec // test binary from the environment
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir, "HOME="+dir)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("caddy log:\n%s", logs.String())
		}
	})
	for i := 0; i < 50; i++ {
		if resp, err := http.Get("http://" + admin + "/config/"); err == nil {
			resp.Body.Close()
			return admin
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("caddy did not start:\n%s", logs.String())
	return ""
}

// loadForTest moves a generated config onto local ports, keeps
// certificates local and loads it.
func loadForTest(t *testing.T, admin string, cfg *caddyConfig, httpPort, httpsPort int) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["admin"] = map[string]any{"listen": admin}
	apps := m["apps"].(map[string]any)
	apps["pki"] = map[string]any{"certificate_authorities": map[string]any{"local": map[string]any{"install_trust": false}}}
	httpApp := apps["http"].(map[string]any)
	httpApp["http_port"], httpApp["https_port"] = httpPort, httpsPort
	for name, s := range httpApp["servers"].(map[string]any) {
		srv := s.(map[string]any)
		port := httpPort
		if name == serverHTTPS {
			port = httpsPort
		}
		srv["listen"] = []string{fmt.Sprintf("127.0.0.1:%d", port)}
		srv["automatic_https"] = map[string]any{"disable_redirects": true}
	}
	body, _ := json.Marshal(m)
	resp, err := http.Post("http://"+admin+"/load", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		t.Fatalf("load: %d %s", resp.StatusCode, b.String())
	}
}

func TestPathRoutesAgainstCaddy(t *testing.T) {
	admin := startCaddy(t)
	plain := httptest.NewServer(echoHandler("plain"))
	defer plain.Close()
	secure := httptest.NewTLSServer(echoHandler("tls"))
	defer secure.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}))

	r := apexSite()
	r.TLSMode = "internal"
	r.Upstream = strings.TrimPrefix(plain.URL, "http://")
	r.Options.Routing.UpstreamTLS = UpstreamTLS{}
	strip := toSite(prefix("/strip"))
	strip.StripPrefix = true
	ownPort := toSite(prefix("/own-port"))
	ownPort.Headers.RequestSet = []Header{{Name: "X-Forwarded-Port", Value: "8443"}}
	backend := PathRoute{Match: prefix("/secure"), Action: ActionProxy,
		Target: PathTarget{Kind: TargetStatic, Upstreams: []string{strings.TrimPrefix(secure.URL, "https://")}, TLS: UpstreamTLS{TLS: true, ServerName: "example.com", CAPEM: ca}}}
	r.Paths = append(r.Paths, strip, ownPort, backend)
	normalizePaths(r.Paths)
	if err := validatePaths(r.Paths); err != nil {
		t.Fatal(err)
	}
	if err := r.Options.validate(r.Host, r.TLSMode); err != nil {
		t.Fatal(err)
	}
	cfg, blocked := buildConfig([]Route{r}, Settings{})
	if len(blocked) > 0 {
		t.Fatalf("blocked %v", blocked)
	}
	httpPort, httpsPort := freePort(t), freePort(t)
	loadForTest(t, admin, cfg, httpPort, httpsPort)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Caddy's internal CA in a test
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				port := httpPort
				if strings.HasSuffix(addr, ":443") {
					port = httpsPort
				}
				return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// get returns the status line and headers; the body is read and
	// closed here.
	type answer struct {
		StatusCode int
		Header     http.Header
	}
	get := func(url string) (answer, echo) {
		t.Helper()
		var resp *http.Response
		var err error
		for i := 0; i < 50; i++ {
			resp, err = client.Get(url)
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		var e echo
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return answer{resp.StatusCode, resp.Header}, e
	}
	const site = "https://apex.example.com"

	t.Run("strip prefix", func(t *testing.T) {
		for path, want := range map[string]string{"/strip": "/", "/strip/x": "/x", "/strip?q=1": "/?q=1", "/strip/a/b?c=d": "/a/b?c=d"} {
			if _, e := get(site + path); e.URI != want {
				t.Errorf("%s reached the backend as %q, want %q", path, e.URI, want)
			}
		}
		if _, e := get(site + "/ords/x"); e.URI != "/ords/x" {
			t.Errorf("unstripped route changed the path: %q", e.URI)
		}
	})

	t.Run("prefix is a folder", func(t *testing.T) {
		resp, _ := get(site + "/ordsfoo")
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ords/r/app/home" {
			t.Errorf("/ordsfoo: %d %s, want the fallback redirect", resp.StatusCode, resp.Header.Get("Location"))
		}
	})

	t.Run("response header order", func(t *testing.T) {
		resp, _ := get(site + "/i/app.js")
		if got := resp.Header.Get("Cache-Control"); got != "public, max-age=86400" {
			t.Errorf("/i Cache-Control = %q, want the route's", got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "" {
			t.Errorf("/i X-Frame-Options = %q, want it removed by the route", got)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Error("/i lost the site's other security headers")
		}
		resp, _ = get(site + "/ords/x")
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("/ords Cache-Control = %q, want the site's", got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("/ords X-Frame-Options = %q, want the site's", got)
		}
	})

	t.Run("forwarded headers", func(t *testing.T) {
		_, e := get(site + "/ords/x")
		if e.Header.Get("X-Forwarded-Port") != "443" || e.Header.Get("X-Forwarded-Proto") != "https" ||
			e.Header.Get("X-Forwarded-Host") != "apex.example.com" || e.Header.Get("X-Forwarded-For") == "" {
			t.Errorf("https headers: %v", e.Header)
		}
		if e.Host != "apex.example.com" || e.Header.Get("X-Env") != "ords" {
			t.Errorf("host %q, X-Env %q", e.Host, e.Header.Get("X-Env"))
		}
		_, e = get("http://apex.example.com/ords/x")
		if e.Header.Get("X-Forwarded-Port") != "80" || e.Header.Get("X-Forwarded-Proto") != "http" {
			t.Errorf("http headers: %v", e.Header)
		}
		if _, e = get(site + "/own-port"); e.Header.Get("X-Forwarded-Port") != "8443" {
			t.Errorf("route header did not replace X-Forwarded-Port: %v", e.Header)
		}
	})

	t.Run("upstream TLS with own CA", func(t *testing.T) {
		resp, e := get(site + "/secure/x")
		if resp.StatusCode != http.StatusOK || e.Backend != "tls" || e.Host != "apex.example.com" {
			t.Errorf("TLS backend: %d %+v", resp.StatusCode, e)
		}
	})
}
