package agents

import (
	"context"
	"errors"
	"testing"
)

func TestUninstalledKeepsRemovedStatus(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	enr, err := svc.Enroll(ctx, enrollReq(res.Token))
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Get(ctx, enr.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	other := newConnectedAgent(enr.AgentID, "edge-1", func() {})
	other.certFP = "not-the-hosts-cert"
	if err := svc.markUninstalled(ctx, other); !errors.Is(err, errNotActive) {
		t.Fatalf("markUninstalled with a foreign cert = %v, want errNotActive", err)
	}

	ag := newConnectedAgent(enr.AgentID, "edge-1", func() {})
	ag.certFP = a.CertFingerprint
	if err := svc.markOnline(ctx, ag, HelloPayload{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.markUninstalled(ctx, ag); err != nil {
		t.Fatal(err)
	}
	svc.markOffline(ag)

	for _, step := range []string{"after the session", "on a second read"} {
		got, err := svc.Get(ctx, enr.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusRemoved || got.OnlineSince != nil {
			t.Fatalf("%s: status %q online_since %v, want %q and none", step, got.Status, got.OnlineSince, StatusRemoved)
		}
	}

	// A re-enrolled host is managed again.
	rot, err := svc.RotateEnrollToken(ctx, enr.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(rot.Token)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, enr.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "offline" {
		t.Fatalf("status after re-enrollment = %q, want offline", got.Status)
	}
}
