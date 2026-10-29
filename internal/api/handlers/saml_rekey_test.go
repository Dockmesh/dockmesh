package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/saml"
)

func TestEndSAMLRekey(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Audit = audit.NewService(e.db, "")
	e.h.SAML = saml.NewService(e.db, e.h.Auth, nil, "")
	if _, err := e.db.Exec(`INSERT INTO users (id, username, email, password, saml_provider, saml_subject, saml_rekey_until)
		VALUES ('u1', 'ann', 'ann@example.com', 'x', 'corp', 'G-1', datetime('now','+30 days'))`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.h.SAML.SyncRekeyNotice(ctx, e.h.Notices); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	e.h.EndSAMLRekey(rr, httptest.NewRequest(http.MethodPost, "/saml/rekey/end", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"ended":1`) {
		t.Fatalf("end: %d %s", rr.Code, rr.Body)
	}
	var marked, entries int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM users WHERE saml_rekey_until IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, audit.ActionSAMLRekeyEnd).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if marked != 0 || entries != 1 {
		t.Errorf("marked accounts = %d, audit entries = %d; want 0 and 1", marked, entries)
	}
	open, err := e.h.Notices.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range open {
		if o.Key == saml.RekeyNoticeKey {
			t.Errorf("re-key notice still open: %+v", o)
		}
	}
}
