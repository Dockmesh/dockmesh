package webtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	for _, v := range []string{"", "internal", "file", "caddy", "proxy"} {
		if _, err := ParseMode(v); err != nil {
			t.Errorf("ParseMode(%q): %v", v, err)
		}
	}
	for _, v := range []string{"https", "INTERNAL", "none"} {
		if _, err := ParseMode(v); err == nil {
			t.Errorf("ParseMode(%q) accepted", v)
		}
	}
}

func TestDefaultTrustedProxies(t *testing.T) {
	tests := []struct {
		mode Mode
		addr string
		want string
	}{
		{Proxy, "127.0.0.1:8080", "127.0.0.1"},
		{Caddy, "127.0.0.1:8080", "127.0.0.1"},
		{Proxy, "10.0.0.5:8080", "10.0.0.5"},
		{Proxy, "[::1]:8080", "::1"},
		{Proxy, ":8080", ""},
		{Proxy, "0.0.0.0:8080", ""},
		{Proxy, "[::]:8080", ""},
		{Proxy, "dm.lan:8080", ""},
		{Plain, "127.0.0.1:8080", ""},
		{Internal, "127.0.0.1:8080", ""},
	}
	for _, tt := range tests {
		if got := DefaultTrustedProxies(tt.mode, tt.addr); got != tt.want {
			t.Errorf("DefaultTrustedProxies(%q, %q) = %q, want %q", tt.mode, tt.addr, got, tt.want)
		}
	}
}

// writePair writes a fresh self-signed pair for name into dir.
func writePair(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestReloader(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir, "one.example")
	r, err := NewReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r.now = func() time.Time { return now }
	if cn := r.Certificate().Leaf.Subject.CommonName; cn != "one.example" {
		t.Fatalf("serves %s", cn)
	}

	writePair(t, dir, "two.example")
	later := now.Add(time.Second)
	if err := os.Chtimes(certFile, later, later); err != nil {
		t.Fatal(err)
	}
	if cn := r.Certificate().Leaf.Subject.CommonName; cn != "one.example" {
		t.Errorf("reloaded before the interval: %s", cn)
	}
	now = now.Add(r.interval)
	if cn := r.Certificate().Leaf.Subject.CommonName; cn != "two.example" {
		t.Errorf("renewed files not reloaded: %s", cn)
	}

	// A renewal caught halfway keeps the previous pair.
	if err := os.WriteFile(keyFile, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(r.interval)
	if cn := r.Certificate().Leaf.Subject.CommonName; cn != "two.example" {
		t.Errorf("broken files replaced the certificate: %s", cn)
	}
}

func TestLoadPairErrors(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir, "one.example")
	other := t.TempDir()
	_, otherKey := writePair(t, other, "two.example")
	tests := []struct {
		name      string
		cert, key string
	}{
		{"missing setting", "", keyFile},
		{"missing file", filepath.Join(dir, "nope.pem"), keyFile},
		{"mismatched key", certFile, otherKey},
		{"directory", dir, keyFile},
	}
	for _, tt := range tests {
		if _, err := LoadPair(tt.cert, tt.key); err == nil {
			t.Errorf("%s: loaded", tt.name)
		}
	}
	if _, err := LoadPair(certFile, keyFile); err != nil {
		t.Errorf("good pair: %v", err)
	}
}
