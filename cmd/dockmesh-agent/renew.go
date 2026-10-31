package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/gorilla/websocket"
)

const renewCheckInterval = 12 * time.Hour

// certStore holds the client cert the agent presents. It is swapped in
// place after a renewal, so the next dial and binary download use the new
// cert without a restart.
type certStore struct {
	certPath, keyPath string
	roots             *x509.CertPool
	cur               atomic.Pointer[tls.Certificate]
}

func (s *certStore) load() error {
	cert, err := tls.LoadX509KeyPair(s.certPath, s.keyPath)
	if err != nil {
		return err
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return err
		}
	}
	s.cur.Store(&cert)
	return nil
}

func (s *certStore) notAfter() time.Time { return s.cur.Load().Leaf.NotAfter }

func (s *certStore) tlsConfig() *tls.Config {
	return &tls.Config{
		RootCAs:    s.roots,
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return s.cur.Load(), nil
		},
	}
}

// finishPendingRenewal completes a renewal that was interrupted between
// writing the new pair and moving it into place. Both .next files are
// complete before the first rename, so a present cert.next means the
// pair can be finished; a lone key.next is an unfinished write.
func finishPendingRenewal(certPath, keyPath string) error {
	if !fileExists(certPath + ".next") {
		if fileExists(keyPath + ".next") {
			return os.Remove(keyPath + ".next")
		}
		return nil
	}
	if fileExists(keyPath + ".next") {
		if err := os.Rename(keyPath+".next", keyPath); err != nil {
			return err
		}
	}
	return os.Rename(certPath+".next", certPath)
}

// renewLoop renews the client cert over the live session once it is
// within agents.RenewBefore of expiry. After a renewal it ends the
// session, so the agent reconnects with the new cert right away and the
// server retires the old one.
func renewLoop(ctx context.Context, conn *websocket.Conn, store *certStore) {
	t := time.NewTicker(renewCheckInterval)
	defer t.Stop()
	for {
		if time.Until(store.notAfter()) < agents.RenewBefore {
			if err := renewCert(ctx, conn, store); err != nil {
				if ctx.Err() == nil {
					slog.Warn("client cert renewal failed", "expires", store.notAfter(), "err", err)
				}
			} else {
				slog.Info("client cert renewed, reconnecting with it", "expires", store.notAfter())
				_ = conn.Close()
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func renewCert(ctx context.Context, conn *websocket.Conn, store *certStore) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	old := store.cur.Load().Leaf
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: old.Subject}, key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res agents.CertRenewRes
	req := agents.CertRenewReq{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))}
	if err := askServer(ctx, conn, agents.FrameAgentCertRenew, req, &res); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := checkRenewedCert([]byte(res.ClientCert), keyPEM, old, store.roots); err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(store.keyPath+".next", keyPEM, 0o600); err != nil {
		return err
	}
	if err := pki.WriteFileAtomic(store.certPath+".next", []byte(res.ClientCert), 0o600); err != nil {
		return err
	}
	if err := finishPendingRenewal(store.certPath, store.keyPath); err != nil {
		return err
	}
	return store.load()
}

// checkRenewedCert accepts the server's answer only if it is a client
// cert for our new key, for the same identity, issued by the pinned CA.
func checkRenewedCert(certPEM, keyPEM []byte, old *x509.Certificate, roots *x509.CertPool) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("renewed cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if leaf.Subject.CommonName != old.Subject.CommonName {
		return errors.New("renewed cert is for a different agent")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return fmt.Errorf("renewed cert: %w", err)
	}
	return nil
}
