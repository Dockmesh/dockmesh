package agents

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConnectRefusalNamesDeletedHostOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		closeDB  bool
		wantCode int
		wantBody string
	}{
		{"unknown certificate", false, http.StatusUnauthorized, "agent not registered"},
		{"database error", true, http.StatusServiceUnavailable, "try again later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, mgr := newTestService(t)
			certPEM, _, _, err := mgr.IssueClientCert("gone", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode(certPEM)
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if tc.closeDB {
				_ = svc.db.Close()
			}
			req := httptest.NewRequest(http.MethodGet, "/connect", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
			rec := httptest.NewRecorder()
			NewWSHandler(svc, mgr).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
			}
		})
	}
}
