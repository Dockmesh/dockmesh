package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dockmesh/dockmesh/internal/pki"
)

func TestSelfProbePinsCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	m, err := pki.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.EnsureUICert(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	tests := []struct {
		name string
		leaf *tls.Certificate
		ok   bool
	}{
		{name: "served certificate", leaf: &srv.TLS.Certificates[0], ok: true},
		{name: "another certificate", leaf: other, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf := tt.leaf.Leaf
			if leaf == nil {
				t.Skip("httptest certificate has no parsed leaf")
			}
			base, client := selfProbe(":"+port, &tls.Config{Certificates: []tls.Certificate{{Leaf: leaf}}})
			if base != "https://127.0.0.1:"+port {
				t.Fatalf("base = %s", base)
			}
			resp, err := client.Get(base + "/")
			if err == nil {
				resp.Body.Close()
			}
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestSelfProbePlain(t *testing.T) {
	tests := []struct{ addr, want string }{
		{":8080", "http://127.0.0.1:8080"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000"},
		{"192.0.2.4:8080", "http://192.0.2.4:8080"},
		{"dm.lan:8080", "http://127.0.0.1:8080"},
	}
	for _, tt := range tests {
		if got, _ := selfProbe(tt.addr, nil); got != tt.want {
			t.Errorf("selfProbe(%q) = %s, want %s", tt.addr, got, tt.want)
		}
	}
}

func TestSetupBaseURLScheme(t *testing.T) {
	t.Setenv("DOCKMESH_BASE_URL", "http://dm.lan:8080/")
	if got := setupBaseURL(":8080", true); got != "https://dm.lan:8080" {
		t.Errorf("https base = %s", got)
	}
	if got := setupBaseURL(":8080", false); got != "http://dm.lan:8080" {
		t.Errorf("http base = %s", got)
	}
}
