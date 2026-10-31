package agents

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/pki"
)

func csrPEM(t *testing.T, key any, cn string) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func enrolledAgent(t *testing.T, svc *Service) (*ConnectedAgent, string) {
	t.Helper()
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	enr, err := svc.Enroll(ctx, enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(enr.ClientCert))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ag := newConnectedAgent(res.Agent.ID, "edge-1", nil)
	ag.certFP = pki.FingerprintFromCert(cert)
	ag.certNotAfter = cert.NotAfter
	return ag, ag.certFP
}

func TestCertRenewalKeepsHostID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ag, oldFP := enrolledAgent(t, svc)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	req, _ := json.Marshal(CertRenewReq{CSR: csrPEM(t, key, "someone-else")})

	if _, err := svc.renewCert(ctx, ag, req); !errors.Is(err, errRenewNotDue) {
		t.Fatalf("renewal of a fresh cert: %v", err)
	}

	ag.certNotAfter = time.Now().Add(10 * 24 * time.Hour)
	res, err := svc.renewCert(ctx, ag, req)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(res.ClientCert))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != ag.ID {
		t.Fatalf("renewed cert CN %q, want host id %q", cert.Subject.CommonName, ag.ID)
	}
	if !cert.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatal("renewed cert is not for the agent's new key")
	}
	newFP := pki.FingerprintFromCert(cert)
	if _, err := svc.renewCert(ctx, ag, req); !errors.Is(err, errRenewedTwice) {
		t.Fatalf("second renewal in one session: %v", err)
	}

	// Both certs work until the agent connects with the new one.
	for _, fp := range []string{oldFP, newFP} {
		a, err := svc.LookupByFingerprint(ctx, fp)
		if err != nil || a.ID != ag.ID {
			t.Fatalf("lookup %s: %v", fp[:8], err)
		}
	}
	if err := svc.recordCert(ctx, ag.ID, newFP, cert.NotAfter); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LookupByFingerprint(ctx, oldFP); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old cert still accepted after the switch: %v", err)
	}
	a, err := svc.Get(ctx, ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.CertExpiresAt == nil || a.CertExpiresAt.Sub(cert.NotAfter).Abs() > time.Second {
		t.Fatalf("cert expiry %v, want %v", a.CertExpiresAt, cert.NotAfter)
	}
}

func TestCertRenewalRejectsBadCSR(t *testing.T) {
	svc, _ := newTestService(t)
	ag, _ := enrolledAgent(t, svc)
	ag.certNotAfter = time.Now().Add(time.Hour)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, csr := range map[string]string{
		"rsa key": csrPEM(t, rsaKey, "x"),
		"garbage": "not a csr",
	} {
		req, _ := json.Marshal(CertRenewReq{CSR: csr})
		if _, err := svc.renewCert(context.Background(), ag, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCertNotices(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	n := notices.NewService(svc.db)
	ag, fp := enrolledAgent(t, svc)

	key := certNoticePrefix + ag.ID
	noticeFor := func() *notices.Notice {
		t.Helper()
		open, err := n.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range open {
			if open[i].Key == key {
				return &open[i]
			}
		}
		return nil
	}

	if err := svc.SyncCertNotices(ctx, n); err != nil {
		t.Fatal(err)
	}
	if noticeFor() != nil {
		t.Fatal("notice for a fresh cert")
	}

	if err := svc.recordCert(ctx, ag.ID, fp, time.Now().Add(5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncCertNotices(ctx, n); err != nil {
		t.Fatal(err)
	}
	if got := noticeFor(); got == nil || got.Severity != notices.SevWarning {
		t.Fatalf("expiring cert: notice %+v", got)
	}

	if err := svc.recordCert(ctx, ag.ID, fp, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncCertNotices(ctx, n); err != nil {
		t.Fatal(err)
	}
	got := noticeFor()
	if got == nil || got.Severity != notices.SevCritical || !strings.Contains(got.Body, "re-run the installer") {
		t.Fatalf("expired offline cert: notice %+v", got)
	}

	if err := svc.Delete(ctx, ag.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncCertNotices(ctx, n); err != nil {
		t.Fatal(err)
	}
	if noticeFor() != nil {
		t.Fatal("notice stayed after the host was deleted")
	}
}

func TestCertRenewalRefusedAfterReenroll(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	ag, _ := enrolledAgent(t, svc)
	ag.certNotAfter = time.Now().Add(time.Hour)

	rot, err := svc.RotateEnrollToken(ctx, ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(rot.Token)); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	req, _ := json.Marshal(CertRenewReq{CSR: csrPEM(t, key, "x")})
	if _, err := svc.renewCert(ctx, ag, req); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renewal on a session with a replaced cert: %v", err)
	}
}
