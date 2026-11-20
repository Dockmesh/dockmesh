package agents

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/pki"
)

// connectAs enrolls an agent called name and records the hello it would
// send, then lets it go offline again.
func connectAs(t *testing.T, svc *Service, name string, hello HelloPayload) *ConnectedAgent {
	t.Helper()
	ctx := context.Background()
	res, err := svc.Create(ctx, name)
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
	ag := newConnectedAgent(res.Agent.ID, name, nil)
	ag.certFP = pki.FingerprintFromCert(cert)
	reconnect(t, svc, ag, hello)
	return ag
}

func reconnect(t *testing.T, svc *Service, ag *ConnectedAgent, hello HelloPayload) {
	t.Helper()
	if err := svc.markOnline(context.Background(), ag, hello); err != nil {
		t.Fatal(err)
	}
	svc.markOffline(ag)
}

func TestLegacyLayoutNotice(t *testing.T) {
	legacy := HelloPayload{OS: "linux"}
	managed := HelloPayload{OS: "linux", Layout: LayoutManaged}
	tests := []struct {
		name     string
		hellos   map[string]HelloPayload
		wantOpen bool
		wantIn   []string
		wantOut  []string
	}{
		{"only managed agents", map[string]HelloPayload{"m1": managed, "m2": managed}, false, nil, nil},
		{"legacy and managed", map[string]HelloPayload{"old-1": legacy, "new-1": managed}, true,
			[]string{"old-1", "Re-run the install command"}, []string{"new-1"}},
		{"hello without a layout counts as legacy", map[string]HelloPayload{"v05": {OS: "linux", Version: "v0.5.4"}}, true,
			[]string{"v05"}, nil},
		{"agents without systemd are left out", map[string]HelloPayload{"mac": {OS: "darwin"}}, false, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			n := notices.NewService(svc.db)
			for name, h := range tt.hellos {
				connectAs(t, svc, name, h)
			}
			// Twice: an unchanged condition leaves the notice as it is.
			var first []notices.Notice
			for i := range 2 {
				if err := svc.SyncLegacyLayoutNotice(ctx, n); err != nil {
					t.Fatal(err)
				}
				got := openLayoutNotices(t, n)
				if i == 0 {
					first = got
				} else if fmt.Sprint(got) != fmt.Sprint(first) {
					t.Fatalf("second sync changed the notice:\n%+v\n%+v", first, got)
				}
			}
			if (len(first) == 1) != tt.wantOpen || len(first) > 1 {
				t.Fatalf("open notices %+v, want open=%v", first, tt.wantOpen)
			}
			if !tt.wantOpen {
				return
			}
			for _, s := range tt.wantIn {
				if !strings.Contains(first[0].Body, s) {
					t.Errorf("body %q lacks %q", first[0].Body, s)
				}
			}
			for _, s := range tt.wantOut {
				if strings.Contains(first[0].Body, s) {
					t.Errorf("body %q names %q", first[0].Body, s)
				}
			}
		})
	}
}

// TestLegacyLayoutNoticeLifecycle relies on the syncs the agent events
// run themselves.
func TestLegacyLayoutNoticeLifecycle(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	n := notices.NewService(svc.db)
	svc.SetNotices(n)
	legacy := HelloPayload{OS: "linux"}
	managed := HelloPayload{OS: "linux", Layout: LayoutManaged}

	// connectAs leaves every agent offline: an agent Docker took down
	// stays listed.
	var ags []*ConnectedAgent
	for i := range 12 {
		ags = append(ags, connectAs(t, svc, fmt.Sprintf("old-%02d", i), legacy))
	}
	got := openLayoutNotices(t, n)
	if len(got) != 1 || !strings.Contains(got[0].Body, "old-09") || strings.Contains(got[0].Body, "old-10") ||
		!strings.Contains(got[0].Body, "and 2 more") {
		t.Fatalf("12 legacy agents: %+v", got)
	}
	if got[0].Link != "/hosts" || got[0].Severity != notices.SevWarning {
		t.Errorf("notice = %+v", got[0])
	}

	admin := notices.Viewer{UserID: "u-admin", Allowed: func(string) bool { return true }}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO users (id, username, password) VALUES ('u-admin', 'admin', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := n.Dismiss(ctx, admin, got[0].ID); err != nil {
		t.Fatal(err)
	}
	reconnect(t, svc, ags[1], legacy)
	if err := svc.SyncLegacyLayoutNotice(ctx, n); err != nil {
		t.Fatal(err)
	}
	if v, err := n.ForViewer(ctx, admin); err != nil || len(v) != 1 || !v[0].Dismissed {
		t.Fatalf("an unchanged layout brought a dismissed notice back: %+v, %v", v, err)
	}

	if err := svc.Delete(ctx, ags[0].ID); err != nil {
		t.Fatal(err)
	}
	got = openLayoutNotices(t, n)
	if len(got) != 1 || strings.Contains(got[0].Body, "old-00") || !strings.Contains(got[0].Body, "and 1 more") {
		t.Fatalf("after deleting one: %+v", got)
	}

	if err := svc.markUninstalled(ctx, ags[1]); err != nil {
		t.Fatal(err)
	}
	if got = openLayoutNotices(t, n); len(got) != 1 || strings.Contains(got[0].Body, "old-01") {
		t.Fatalf("after one removed itself: %+v", got)
	}

	for _, ag := range ags[2:] {
		reconnect(t, svc, ag, managed)
	}
	if got := openLayoutNotices(t, n); len(got) != 0 {
		t.Fatalf("all agents moved to the managed layout, notice still open: %+v", got)
	}
	if err := svc.SyncLegacyLayoutNotice(ctx, n); err != nil {
		t.Fatal(err)
	}
	if got := openLayoutNotices(t, n); len(got) != 0 {
		t.Fatalf("the periodic sync reopened the notice: %+v", got)
	}
}

func openLayoutNotices(t *testing.T, n *notices.Service) []notices.Notice {
	t.Helper()
	open, err := n.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []notices.Notice
	for _, o := range open {
		if o.Key == legacyLayoutNoticeKey {
			out = append(out, o)
		}
	}
	return out
}
