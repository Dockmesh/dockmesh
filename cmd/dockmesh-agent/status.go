package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runStatusCmd implements `dockmesh-agent status`. It reads the local
// cert/key/ca files, prints cert expiry, and tries a quick TLS dial to
// the server to confirm reachability.
func runStatusCmd() {
	dataDir := envOr("DOCKMESH_DATA_DIR", "/var/lib/dockmesh")
	certPath := filepath.Join(dataDir, "agent.crt")
	keyPath := filepath.Join(dataDir, "agent.key")
	caPath := filepath.Join(dataDir, "ca.crt")
	urlPath := filepath.Join(dataDir, "agent.url")

	fmt.Printf("dockmesh-agent %s\n", agentVersion)
	fmt.Printf("data dir: %s\n\n", dataDir)

	enrolled := fileExists(certPath) && fileExists(keyPath) && fileExists(caPath)
	if !enrolled {
		fmt.Println("status: NOT ENROLLED")
		fmt.Println("  missing cert/key/ca — run with DOCKMESH_ENROLL_URL + DOCKMESH_TOKEN set to enroll")
		return
	}

	fmt.Println("status: enrolled")

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fmt.Printf("  cert read error: %v\n", err)
		return
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		fmt.Println("  cert file has no PEM block")
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		fmt.Printf("  cert parse error: %v\n", err)
		return
	}
	remaining := time.Until(cert.NotAfter)
	expMark := "ok"
	switch {
	case remaining < 0:
		expMark = "EXPIRED"
	case remaining < 14*24*time.Hour:
		expMark = "expiring soon"
	}
	fmt.Printf("  cert subject:  %s\n", cert.Subject.CommonName)
	fmt.Printf("  cert expires:  %s (%s — %s)\n",
		cert.NotAfter.Format(time.RFC3339),
		shortDuration(remaining), expMark)

	dialURL := os.Getenv("DOCKMESH_AGENT_URL")
	if dialURL == "" {
		if b, err := os.ReadFile(urlPath); err == nil {
			dialURL = strings.TrimSpace(string(b))
		}
	}
	if dialURL == "" {
		fmt.Println("  server url:    (not configured)")
		return
	}
	fmt.Printf("  server url:    %s\n", dialURL)

	u, err := url.Parse(dialURL)
	if err != nil || u.Host == "" {
		fmt.Printf("  reachability:  cannot parse url: %v\n", err)
		return
	}

	// Quick TLS dial to confirm the listener is up. We skip cert
	// verification here because this command's purpose is "can we
	// reach it at all?" — the normal connect loop does full mTLS.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		fmt.Printf("  reachability:  FAIL (%v)\n", err)
		return
	}
	_ = conn.Close()
	fmt.Println("  reachability:  ok (tls handshake succeeded)")
}

// shortDuration renders a duration like "14d 3h" for cert expiry.
func shortDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
