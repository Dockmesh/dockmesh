package setup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckPort(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port
	free, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	tests := []struct {
		name  string
		port  int
		own   []int
		issue string
	}{
		{name: "busy", port: busy, issue: IssuePortBusy},
		{name: "own listener", port: busy, own: []int{busy}},
		{name: "free", port: freePort},
		{name: "zero", port: 0, issue: IssuePortInvalid},
		{name: "too high", port: 65536, issue: IssuePortInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckPort(tt.port, tt.own...)
			if got.Issue != tt.issue || (got.Status == "ok") != (tt.issue == "") {
				t.Errorf("CheckPort(%d) = %+v, want issue %q", tt.port, got, tt.issue)
			}
		})
	}
}

// writeTestPair writes a self-signed pair for dm.example.org valid until
// notAfter.
func writeTestPair(t *testing.T, dir, name string, notAfter time.Time) (string, string) {
	t.Helper()
	const host = "dm.example.org"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, keyFile := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, keyFile
}

func TestCheckCertPair(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	good, goodKey := writeTestPair(t, dir, "good", now.Add(90*24*time.Hour))
	_, otherKey := writeTestPair(t, dir, "other", now.Add(90*24*time.Hour))
	old, oldKey := writeTestPair(t, dir, "old", now.Add(-24*time.Hour))
	soon, soonKey := writeTestPair(t, dir, "soon", now.Add(10*24*time.Hour+time.Hour))
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked.pem")
	if err := os.WriteFile(locked, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, cert, key, host string
		status, issue         string
	}{
		{name: "good", cert: good, key: goodKey, host: "dm.example.org", status: "ok"},
		{name: "mismatch", cert: good, key: otherKey, status: "fail", issue: IssueCertMismatch},
		{name: "expired", cert: old, key: oldKey, status: "fail", issue: IssueCertExpired},
		{name: "other host", cert: good, key: goodKey, host: "other.example.org", status: "warn", issue: IssueCertHostname},
		{name: "ends soon", cert: soon, key: soonKey, host: "dm.example.org", status: "warn", issue: IssueCertExpiring},
		{name: "missing", cert: filepath.Join(dir, "nope.pem"), key: goodKey, status: "fail", issue: IssueCertMissing},
		{name: "not a pair", cert: junk, key: goodKey, status: "fail", issue: IssueCertMismatch},
		{name: "a directory", cert: dir, key: goodKey, status: "fail", issue: IssueCertInvalid},
	}
	if os.Geteuid() != 0 {
		tests = append(tests, struct {
			name, cert, key, host string
			status, issue         string
		}{name: "unreadable", cert: locked, key: goodKey, status: "fail", issue: IssueCertUnreadable})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckCertPair(tt.cert, tt.key, tt.host, now)
			if got.Status != tt.status || got.Issue != tt.issue {
				t.Errorf("CheckCertPair = %+v, want %s/%s", got, tt.status, tt.issue)
			}
			if tt.issue == IssueCertExpiring && got.Days != 10 {
				t.Errorf("days = %d, want 10", got.Days)
			}
		})
	}
	if expired, end, err := CertExpired(old, now); err != nil || !expired || end.After(now) {
		t.Errorf("CertExpired(old) = %v %v %v", expired, end, err)
	}
	if expired, _, err := CertExpired(good, now); err != nil || expired {
		t.Errorf("CertExpired(good) = %v %v", expired, err)
	}
}
