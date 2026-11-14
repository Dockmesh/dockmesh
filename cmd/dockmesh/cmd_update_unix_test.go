//go:build unix

package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/webtls"
)

func TestHealthURL(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"", "http://127.0.0.1:8080/api/v1/health"},
		{":8080", "http://127.0.0.1:8080/api/v1/health"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/api/v1/health"},
		{"[::]:9000", "http://[::1]:9000/api/v1/health"},
		{"192.168.1.5:8080", "http://192.168.1.5:8080/api/v1/health"},
		{"[fd00::5]:8080", "http://[fd00::5]:8080/api/v1/health"},
		{"evil.example.com:8080", "http://127.0.0.1:8080/api/v1/health"},
		{"garbage", "http://127.0.0.1:8080/api/v1/health"},
	}
	for _, tt := range tests {
		if got := healthURL("http", tt.addr); got != tt.want {
			t.Errorf("healthURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestServerFiles(t *testing.T) {
	files := serverFiles("/usr/local/bin/dockmesh")
	if !files[0].KeepPrev || files[0].Optional || files[0].Dest != "/usr/local/bin/dockmesh" {
		t.Fatalf("first file %+v", files[0])
	}
	want := map[string]string{
		"dmctl":                      "/usr/local/bin/dmctl",
		"dockmesh-agent-linux-amd64": "/usr/local/share/dockmesh/bin/dockmesh-agent-linux-amd64",
		"install-agent.sh":           "/usr/local/share/dockmesh/install-agent.sh",
	}
	for _, f := range files[1:] {
		if !f.Optional || f.KeepPrev {
			t.Errorf("%s: %+v", f.Name, f)
		}
		if w, ok := want[f.Name]; ok && f.Dest != w {
			t.Errorf("%s goes to %s, want %s", f.Name, f.Dest, w)
		}
	}
}

func TestHealthProbeFollowsListener(t *testing.T) {
	dataDir := t.TempDir()
	paths := config.ResolvePaths(func(k string) string {
		if k == "DOCKMESH_DATA_DIR" {
			return dataDir
		}
		return ""
	})
	m, err := pki.New(filepath.Dir(paths.DBPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := m.EnsureUICert(nil)
	if err != nil {
		t.Fatal(err)
	}
	certDir := t.TempDir()
	certFile := filepath.Join(certDir, "cert.pem")
	keyFile := filepath.Join(certDir, "key.pem")
	other, err := pki.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, other.ServerCertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, other.ServerKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	own, err := webtls.LoadPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mode   string
		serve  *tls.Certificate // nil: plain HTTP
		ok     bool
		scheme string
	}{
		{name: "plain", ok: true, scheme: "http"},
		{name: "proxy", mode: "proxy", ok: true, scheme: "http"},
		{name: "internal", mode: "internal", serve: internal, ok: true, scheme: "https"},
		{name: "file", mode: "file", serve: own, ok: true, scheme: "https"},
		{name: "internal mode, other certificate served", mode: "internal", serve: own, ok: false, scheme: "https"},
		{name: "file mode, plain listener", mode: "file", ok: false, scheme: "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			if tt.serve != nil {
				srv.TLS = &tls.Config{Certificates: []tls.Certificate{*tt.serve}}
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()
			env := map[string]string{
				"DOCKMESH_TLS_MODE":      tt.mode,
				"DOCKMESH_HTTP_ADDR":     srv.Listener.Addr().String(),
				"DOCKMESH_TLS_CERT_FILE": certFile,
			}
			url, client, err := healthProbe(env, paths)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(url, tt.scheme+"://127.0.0.1:") {
				t.Fatalf("url %s", url)
			}
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
			}
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestHealthProbeDefaults(t *testing.T) {
	url, _, err := healthProbe(map[string]string{"DOCKMESH_TLS_MODE": "proxy"}, config.Paths{})
	if err != nil || url != "http://127.0.0.1:8080/api/v1/health" {
		t.Errorf("proxy default: %s %v", url, err)
	}
	if _, _, err := healthProbe(map[string]string{"DOCKMESH_TLS_MODE": "bogus"}, config.Paths{}); err == nil {
		t.Error("unknown mode accepted")
	}
	if _, _, err := healthProbe(map[string]string{"DOCKMESH_TLS_MODE": "internal"}, config.ResolvePaths(func(string) string { return "" })); err == nil {
		t.Error("internal mode without a certificate accepted")
	}
}
