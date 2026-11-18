package setup

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/webtls"
)

// PortCheck is the live check of one listener port.
type PortCheck struct {
	Port   int    `json:"port"`
	Status string `json:"status"` // "ok" | "fail"
	Issue  string `json:"issue,omitempty"`
}

// Port issues.
const (
	IssuePortInvalid = "invalid"
	IssuePortBusy    = "busy"
)

// CheckPort reports whether another program listens on port. A port in
// own is one this setup mode listens on itself, which the new config
// takes over at the restart. A port the service may not bind (below 1024
// without the capability) is not reported busy; the start after the
// apply checks it again.
func CheckPort(port int, own ...int) PortCheck {
	c := PortCheck{Port: port, Status: "ok"}
	if port < 1 || port > 65535 {
		c.Status, c.Issue = "fail", IssuePortInvalid
		return c
	}
	for _, p := range own {
		if p == port {
			return c
		}
	}
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err == nil {
		_ = l.Close()
		return c
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		c.Status, c.Issue = "fail", IssuePortBusy
	}
	return c
}

// CertCheck is the live check of an own certificate and key.
type CertCheck struct {
	Status   string    `json:"status"` // "ok" | "warn" | "fail"
	Issue    string    `json:"issue,omitempty"`
	File     string    `json:"file,omitempty"`
	NotAfter time.Time `json:"not_after,omitempty"`
	Days     int       `json:"days,omitempty"`
	Message  string    `json:"message"`
}

// Certificate issues.
const (
	IssueCertUnreadable = "unreadable"
	IssueCertMissing    = "missing"
	IssueCertInvalid    = "invalid"
	IssueCertMismatch   = "mismatch"
	IssueCertExpired    = "expired"
	IssueCertHostname   = "hostname"
	IssueCertExpiring   = "expiring"
)

// certWarnDays is how close to its end a certificate gets a warning.
const certWarnDays = 30

// CheckCertPair checks the certificate and key files as the account
// that runs it, which is the service account in setup mode: readable,
// a matching pair, not expired. A certificate that does not cover host or
// ends within 30 days is a warning. It reads nothing but the two files
// and reports only facts of the certificate.
func CheckCertPair(certFile, keyFile, host string, now time.Time) CertCheck {
	for _, f := range []string{certFile, keyFile} {
		if c, ok := checkPEMFile(f); !ok {
			return c
		}
	}
	pair, err := webtls.LoadPair(certFile, keyFile)
	if err != nil {
		return CertCheck{Status: "fail", Issue: IssueCertMismatch, Message: "The key does not belong to this certificate."}
	}
	return checkLeaf(pair, host, now)
}

func checkPEMFile(path string) (CertCheck, bool) {
	fail := func(issue, format string) (CertCheck, bool) {
		return CertCheck{Status: "fail", Issue: issue, File: path, Message: fmt.Sprintf(format, path)}, false
	}
	_, err := webtls.ReadPEM(path)
	switch {
	case err == nil:
		return CertCheck{}, true
	case errors.Is(err, fs.ErrPermission):
		return fail(IssueCertUnreadable, "The dockmesh user cannot read %s.")
	case errors.Is(err, fs.ErrNotExist):
		return fail(IssueCertMissing, "%s does not exist.")
	default:
		return fail(IssueCertInvalid, "%s is not a PEM file Dockmesh can read.")
	}
}

func checkLeaf(pair *tls.Certificate, host string, now time.Time) CertCheck {
	leaf := pair.Leaf
	c := CertCheck{Status: "ok", NotAfter: leaf.NotAfter.UTC(), Message: "The certificate and key match."}
	if now.After(leaf.NotAfter) {
		c.Status, c.Issue = "fail", IssueCertExpired
		c.Message = "This certificate expired on " + leaf.NotAfter.UTC().Format("2006-01-02") + "."
		return c
	}
	if host != "" && leaf.VerifyHostname(host) != nil {
		c.Status, c.Issue = "warn", IssueCertHostname
		c.Message = "The certificate does not cover " + host + ". Browsers will warn."
		return c
	}
	if left := leaf.NotAfter.Sub(now); left < certWarnDays*24*time.Hour {
		c.Status, c.Issue = "warn", IssueCertExpiring
		c.Days = int(left.Hours() / 24)
		c.Message = fmt.Sprintf("This certificate expires on %s (in %d days).", leaf.NotAfter.UTC().Format("2006-01-02"), c.Days)
	}
	return c
}

// CertExpired reports a certificate file whose first certificate has
// ended, for root's apply after the service read the pair itself.
func CertExpired(certFile string, now time.Time) (bool, time.Time, error) {
	leaf, err := webtls.ReadLeaf(certFile)
	if err != nil {
		return false, time.Time{}, err
	}
	return now.After(leaf.NotAfter), leaf.NotAfter, nil
}
