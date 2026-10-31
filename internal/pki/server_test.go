package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestParseSAN(t *testing.T) {
	tests := []struct {
		in      string
		wantIP  string
		wantDNS string
		wantErr bool
	}{
		{in: "192.0.2.10", wantIP: "192.0.2.10"},
		{in: " 198.51.100.7 ", wantIP: "198.51.100.7"},
		{in: "2001:db8::1", wantIP: "2001:db8::1"},
		{in: "[2001:db8::2]", wantIP: "2001:db8::2"},
		{in: "::ffff:192.0.2.1", wantIP: "192.0.2.1"},
		{in: "Dockmesh.Example.COM", wantDNS: "dockmesh.example.com"},
		{in: "dockmesh.example.com.", wantDNS: "dockmesh.example.com"},
		{in: "dr-host", wantDNS: "dr-host"},
		{in: "*.example.com", wantDNS: "*.example.com"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "https://dockmesh.example.com", wantErr: true},
		{in: "dockmesh.example.com:8443", wantErr: true},
		{in: "192.0.2.10:8443", wantErr: true},
		{in: "host/path", wantErr: true},
		{in: "bad host", wantErr: true},
		{in: "a..b", wantErr: true},
		{in: "-bad.example.com", wantErr: true},
		{in: "bad-.example.com", wantErr: true},
		{in: "under_score.example.com", wantErr: true},
		{in: "*.com", wantErr: true},
		{in: "a.*.example.com", wantErr: true},
		{in: "10.0.0", wantErr: true},
		{in: "999.1.1.1", wantErr: true},
		{in: string(bytes.Repeat([]byte("a"), 64)) + ".example.com", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			ip, dns, err := parseSAN(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSAN(%q) = %v, %q; want error", tt.in, ip, dns)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSAN(%q): %v", tt.in, err)
			}
			gotIP := ""
			if ip != nil {
				gotIP = ip.String()
			}
			if gotIP != tt.wantIP || dns != tt.wantDNS {
				t.Fatalf("parseSAN(%q) = ip %q dns %q; want ip %q dns %q", tt.in, gotIP, dns, tt.wantIP, tt.wantDNS)
			}
		})
	}
}

func TestMergeSANs(t *testing.T) {
	dns, ips := mergeSANs(
		[]string{"a.example.com", "A.example.com", "", "b.example.com"},
		[]net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("::ffff:192.0.2.1"), nil, net.ParseIP("2001:db8::1")},
	)
	if want := []string{"a.example.com", "b.example.com"}; !slices.Equal(dns, want) {
		t.Fatalf("dns = %v, want %v", dns, want)
	}
	var got []string
	for _, ip := range ips {
		got = append(got, ip.String())
	}
	if want := []string{"192.0.2.1", "2001:db8::1"}; !slices.Equal(got, want) {
		t.Fatalf("ips = %v, want %v", got, want)
	}
}

var oldHosts = []string{"old.example.com", "192.0.2.10"}

func newTestPKI(t *testing.T) (string, *Manager) {
	t.Helper()
	dir := t.TempDir()
	m, err := New(dir, oldHosts)
	if err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// verifyServerCert checks the on-disk pair is usable and that the cert
// chains to the CA for every host.
func verifyServerCert(t *testing.T, dir string, hosts []string) *x509.Certificate {
	t.Helper()
	certPEM := readFile(t, filepath.Join(dir, serverCertFile))
	keyPEM := readFile(t, filepath.Join(dir, serverKeyFile))
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("key does not match cert: %v", err)
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(readFile(t, filepath.Join(dir, caCertFile)))
	for _, h := range hosts {
		if _, err := cert.Verify(x509.VerifyOptions{DNSName: h, Roots: roots}); err != nil {
			t.Errorf("cert does not verify for %q: %v", h, err)
		}
	}
	return cert
}

func TestReissueServerCert(t *testing.T) {
	tests := []struct {
		name        string
		add         []string
		wantAdded   []string
		wantPresent []string
		wantChanged bool
		wantErr     bool
		verifyHosts []string
	}{
		{
			name:        "new ip",
			add:         []string{"203.0.113.5"},
			wantAdded:   []string{"203.0.113.5"},
			wantChanged: true,
			verifyHosts: []string{"203.0.113.5", "old.example.com", "192.0.2.10", "localhost", "127.0.0.1"},
		},
		{
			name:        "ip and dns mixed with a present one",
			add:         []string{"New.Example.com", "2001:db8::5", "old.example.com"},
			wantAdded:   []string{"new.example.com", "2001:db8::5"},
			wantPresent: []string{"old.example.com"},
			wantChanged: true,
			verifyHosts: []string{"new.example.com", "2001:db8::5", "old.example.com", "192.0.2.10"},
		},
		{
			name:        "already covered",
			add:         []string{"192.0.2.10", "OLD.example.com"},
			wantPresent: []string{"old.example.com", "192.0.2.10"},
			verifyHosts: []string{"old.example.com", "192.0.2.10"},
		},
		{
			name:    "invalid input",
			add:     []string{"203.0.113.5", "https://new.example.com:8443"},
			wantErr: true,
		},
		{
			name:    "no input",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := newTestPKI(t)
			oldCert := readFile(t, filepath.Join(dir, serverCertFile))
			oldKey := readFile(t, filepath.Join(dir, serverKeyFile))
			oldCA := readFile(t, filepath.Join(dir, caCertFile))
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

			res, err := ReissueServerCert(dir, tt.add, now)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				if !bytes.Equal(readFile(t, filepath.Join(dir, serverCertFile)), oldCert) {
					t.Fatal("cert changed despite error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(res.Added, tt.wantAdded) || !slices.Equal(res.Present, tt.wantPresent) {
				t.Fatalf("added %v present %v; want %v %v", res.Added, res.Present, tt.wantAdded, tt.wantPresent)
			}
			if res.Changed() != tt.wantChanged {
				t.Fatalf("Changed() = %v, want %v", res.Changed(), tt.wantChanged)
			}
			if !bytes.Equal(readFile(t, filepath.Join(dir, caCertFile)), oldCA) {
				t.Fatal("CA cert changed")
			}
			verifyServerCert(t, dir, tt.verifyHosts)

			backupCert := filepath.Join(dir, serverCertFile+".bak-20261030T120000Z")
			backupKey := filepath.Join(dir, serverKeyFile+".bak-20261030T120000Z")
			if !tt.wantChanged {
				if _, err := os.Stat(backupCert); !os.IsNotExist(err) {
					t.Fatalf("backup written for a no-op: %v", err)
				}
				if !bytes.Equal(readFile(t, filepath.Join(dir, serverCertFile)), oldCert) {
					t.Fatal("cert rewritten for a no-op")
				}
				return
			}
			if res.BackupCert != backupCert || res.BackupKey != backupKey {
				t.Fatalf("backups %q %q", res.BackupCert, res.BackupKey)
			}
			if !bytes.Equal(readFile(t, backupCert), oldCert) || !bytes.Equal(readFile(t, backupKey), oldKey) {
				t.Fatal("backup content differs from the old pair")
			}
			if bytes.Equal(readFile(t, filepath.Join(dir, serverCertFile)), oldCert) {
				t.Fatal("cert not replaced")
			}
			if runtime.GOOS != "windows" {
				fi, err := os.Stat(filepath.Join(dir, serverKeyFile))
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode().Perm() != 0o600 {
					t.Fatalf("key mode %o, want 600", fi.Mode().Perm())
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if filepath.Ext(e.Name()) != "" && e.Name()[0] == '.' {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}

			// A server start with the unchanged env SANs must serve the new
			// cert as-is rather than re-issue it.
			newCert := readFile(t, filepath.Join(dir, serverCertFile))
			m, err := New(dir, oldHosts)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(m.ServerCertPEM(), newCert) {
				t.Fatal("server start did not pick up the reissued cert")
			}
			for _, h := range tt.wantAdded {
				if !m.ServerCertCovers(h) {
					t.Errorf("ServerCertCovers(%q) = false", h)
				}
			}
		})
	}
}

func TestReissueServerCertWithoutCA(t *testing.T) {
	if _, err := ReissueServerCert(t.TempDir(), []string{"203.0.113.5"}, time.Now()); err == nil {
		t.Fatal("want error without a CA")
	}
}

func TestReissueServerCertSameSecondDoesNotOverwriteBackup(t *testing.T) {
	dir, _ := newTestPKI(t)
	now := time.Now()
	if _, err := ReissueServerCert(dir, []string{"203.0.113.5"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ReissueServerCert(dir, []string{"203.0.113.6"}, now); err == nil {
		t.Fatal("want error when the backup name already exists")
	}
	verifyServerCert(t, dir, []string{"203.0.113.5"})
}

func TestEnsureServerCert(t *testing.T) {
	otherKeyPEM := func(t *testing.T) []byte {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	}
	tests := []struct {
		name        string
		mutate      func(t *testing.T, dir string)
		hosts       []string
		wantReissue bool
		verifyHosts []string
	}{
		{
			name:        "covered cert is reused",
			hosts:       oldHosts,
			verifyHosts: oldHosts,
		},
		{
			name:        "new env SAN keeps old SANs",
			hosts:       []string{"203.0.113.9"},
			wantReissue: true,
			verifyHosts: []string{"203.0.113.9", "old.example.com", "192.0.2.10"},
		},
		{
			name: "mismatched key is repaired",
			mutate: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, serverKeyFile), otherKeyPEM(t), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			hosts:       oldHosts,
			wantReissue: true,
			verifyHosts: oldHosts,
		},
		{
			name: "read-only key does not block re-issue",
			mutate: func(t *testing.T, dir string) {
				if err := os.Chmod(filepath.Join(dir, serverKeyFile), 0o400); err != nil {
					t.Fatal(err)
				}
			},
			hosts:       []string{"203.0.113.9"},
			wantReissue: true,
			verifyHosts: []string{"203.0.113.9", "old.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := newTestPKI(t)
			if tt.mutate != nil {
				tt.mutate(t, dir)
			}
			before := readFile(t, filepath.Join(dir, serverCertFile))
			m, err := New(dir, tt.hosts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tls.X509KeyPair(m.ServerCertPEM(), m.ServerKeyPEM()); err != nil {
				t.Fatalf("in-memory pair unusable: %v", err)
			}
			after := readFile(t, filepath.Join(dir, serverCertFile))
			if reissued := !bytes.Equal(before, after); reissued != tt.wantReissue {
				t.Fatalf("reissued = %v, want %v", reissued, tt.wantReissue)
			}
			verifyServerCert(t, dir, tt.verifyHosts)
		})
	}
}

func TestServerCertCovers(t *testing.T) {
	_, m := newTestPKI(t)
	tests := []struct {
		host string
		want bool
	}{
		{"old.example.com", true},
		{"OLD.example.com", true},
		{"192.0.2.10", true},
		{"localhost", true},
		{"[::1]", true},
		{"new.example.com", false},
		{"203.0.113.5", false},
	}
	for _, tt := range tests {
		if got := m.ServerCertCovers(tt.host); got != tt.want {
			t.Errorf("ServerCertCovers(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}
