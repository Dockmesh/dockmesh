package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// connectWith runs the parts of a connection that touch the setup state:
// markOnline and the hello check, plus the apply it starts, answered with
// answer (nil: no frame expected).
func connectWith(t *testing.T, svc *Service, id, fp string, hello HelloPayload, answer *StacksDirApplyRes) {
	t.Helper()
	ctx := context.Background()
	ag := newConnectedAgent(id, "edge-1", func() {})
	ag.certFP = fp
	ag.Hello = hello
	if err := svc.markOnline(ctx, ag, hello); err != nil {
		t.Fatal(err)
	}
	defer svc.markOffline(ag)
	p, err := svc.setupFromHello(ctx, id, hello)
	if err != nil {
		t.Fatal(err)
	}
	if p == "" {
		if answer != nil {
			t.Fatal("no stacks dir request was started")
		}
		return
	}
	if answer == nil {
		t.Fatalf("unexpected stacks dir request for %s", p)
	}
	done := make(chan struct{})
	go func() { svc.applyStacksDir(ctx, ag, p); close(done) }()
	f := nextFrame(t, ag)
	if f.Type != FrameReqStacksDirApply {
		t.Fatalf("frame %q, want %q", f.Type, FrameReqStacksDirApply)
	}
	var req StacksDirApplyReq
	if err := json.Unmarshal(f.Payload, &req); err != nil || req.Path != p || req.CheckOnly {
		t.Fatalf("request %+v (%v), want an apply of %s", req, err, p)
	}
	data, _ := json.Marshal(answer)
	payload, _ := json.Marshal(ResponseEnvelope{OK: true, Data: data})
	ag.deliverResponse(Frame{Type: FrameRes, ID: f.ID, Payload: payload})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not finish")
	}
}

func createdHost(ctx context.Context, t *testing.T, svc *Service, o CreateOptions) (id, fp string) {
	t.Helper()
	res, err := svc.CreateWith(ctx, o)
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
	return a.ID, a.CertFingerprint
}

func mustGet(ctx context.Context, t *testing.T, svc *Service, id string) *Agent {
	t.Helper()
	a, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func defaultDirHello() HelloPayload {
	return HelloPayload{Layout: LayoutManaged, StacksDirs: []StacksDirInfo{{Path: "/var/lib/dockmesh-agent/stacks", Current: true}}}
}

func TestNewHostWaitsForSetupUntilConfirmed(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	id, fp := createdHost(context.Background(), t, svc, CreateOptions{Name: "edge-1"})

	for start := 1; start <= 2; start++ {
		connectWith(t, svc, id, fp, defaultDirHello(), nil)
		if a := mustGet(context.Background(), t, svc, id); !a.NeedsSetup || a.StacksDir != nil {
			t.Fatalf("start %d: needs_setup %v stacks_dir %+v, want waiting without a request", start, a.NeedsSetup, a.StacksDir)
		}
	}
	err := svc.SetupError(ctx, id)
	var w *WaitingForSetupError
	if !errors.As(err, &w) || !errors.Is(err, ErrWaitingForSetup) {
		t.Fatalf("SetupError = %v, want a WaitingForSetupError", err)
	}
	if want := "Host edge-1 is waiting for setup. Choose its stacks directory first: Hosts › edge-1 › Setup."; err.Error() != want {
		t.Fatalf("message %q, want %q", err, want)
	}

	if err := svc.ConfirmSetup(ctx, id); err != nil {
		t.Fatal(err)
	}
	for start := 1; start <= 2; start++ {
		connectWith(t, svc, id, fp, defaultDirHello(), nil)
		if a := mustGet(context.Background(), t, svc, id); a.NeedsSetup {
			t.Fatalf("start %d after confirm: still waiting", start)
		}
	}
	if err := svc.SetupError(ctx, id); err != nil {
		t.Fatalf("SetupError after confirm = %v", err)
	}
}

// Hosts enrolled before needs_setup existed have the column's default.
func TestEnrolledHostNeverWaitsForSetup(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.db.ExecContext(ctx, `
		INSERT INTO agents (id, name, status, cert_fingerprint) VALUES ('old-1', 'old-1', 'offline', 'fp-old')`); err != nil {
		t.Fatal(err)
	}
	for start := 1; start <= 2; start++ {
		connectWith(t, svc, "old-1", "fp-old", HelloPayload{}, nil)
		connectWith(t, svc, "old-1", "fp-old", defaultDirHello(), nil)
		if a := mustGet(context.Background(), t, svc, "old-1"); a.NeedsSetup || a.StacksDir != nil {
			t.Fatalf("start %d: needs_setup %v stacks_dir %+v", start, a.NeedsSetup, a.StacksDir)
		}
	}
	if err := svc.SetupError(ctx, "old-1"); err != nil {
		t.Fatalf("SetupError = %v", err)
	}
	rot, err := svc.RotateEnrollToken(ctx, "old-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(rot.Token)); err != nil {
		t.Fatal(err)
	}
	if a := mustGet(context.Background(), t, svc, "old-1"); a.NeedsSetup || a.Name != "old-1" {
		t.Fatalf("after re-enroll: needs_setup %v name %q", a.NeedsSetup, a.Name)
	}
}

func TestStacksDirFromInstallSkipsSetup(t *testing.T) {
	svc, _ := newTestService(t)
	id, fp := createdHost(context.Background(), t, svc, CreateOptions{Name: "edge-1"})
	hello := HelloPayload{Layout: LayoutManaged, StacksDirSet: true, StacksDirs: []StacksDirInfo{{Path: "/srv/stacks", Current: true}}}
	for start := 1; start <= 2; start++ {
		connectWith(t, svc, id, fp, hello, nil)
		if a := mustGet(context.Background(), t, svc, id); a.NeedsSetup {
			t.Fatalf("start %d: still waiting", start)
		}
	}
}

func TestStacksDirFromAPIIsAppliedOnFirstConnect(t *testing.T) {
	tests := []struct {
		name       string
		answer     StacksDirApplyRes
		wantStatus string
		wantError  string
		wantWait   bool
	}{
		{"done", StacksDirApplyRes{Status: StacksDirDone}, StacksDirStateApplied, "", false},
		{"refused", StacksDirApplyRes{Status: StacksDirRefused, Reason: "/srv/stacks is not empty"}, StacksDirStateRefused, "/srv/stacks is not empty", true},
		{"root still busy", StacksDirApplyRes{Status: StacksDirPending}, StacksDirStatePending, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t)
			id, fp := createdHost(context.Background(), t, svc, CreateOptions{Name: "edge-1", StacksDir: "/srv/stacks"})
			if a := mustGet(context.Background(), t, svc, id); !a.NeedsSetup || a.StacksDir == nil || a.StacksDir.Status != StacksDirStatePending {
				t.Fatalf("before connect: needs_setup %v stacks_dir %+v", a.NeedsSetup, a.StacksDir)
			}
			connectWith(t, svc, id, fp, defaultDirHello(), &tc.answer)
			a := mustGet(context.Background(), t, svc, id)
			if a.NeedsSetup != tc.wantWait || a.StacksDir.Status != tc.wantStatus || a.StacksDir.Error != tc.wantError {
				t.Fatalf("needs_setup %v stacks_dir %+v, want %v %s %q", a.NeedsSetup, a.StacksDir, tc.wantWait, tc.wantStatus, tc.wantError)
			}

			// Second start: an answered request is not sent again; a
			// pending one is, and the agent now reports the new dir.
			moved := HelloPayload{Layout: LayoutManaged, StacksDirSet: true, StacksDirs: []StacksDirInfo{{Path: "/srv/stacks", Current: true}}}
			switch tc.wantStatus {
			case StacksDirStatePending:
				connectWith(t, svc, id, fp, moved, nil)
				a = mustGet(context.Background(), t, svc, id)
				if a.NeedsSetup || a.StacksDir.Status != StacksDirStateApplied {
					t.Fatalf("after the restart: needs_setup %v stacks_dir %+v, want applied", a.NeedsSetup, a.StacksDir)
				}
			default:
				before := *a.StacksDir
				connectWith(t, svc, id, fp, defaultDirHello(), nil)
				a = mustGet(context.Background(), t, svc, id)
				if *a.StacksDir != before || a.NeedsSetup != tc.wantWait {
					t.Fatalf("second start changed the state: %+v → %+v", before, *a.StacksDir)
				}
			}
		})
	}
}

func TestHostWithoutNameTakesTheHostname(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	enroll := func(hostname string) *Agent {
		t.Helper()
		res, err := svc.CreateWith(ctx, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(res.Agent.Name, "new-host-") {
			t.Fatalf("placeholder name %q", res.Agent.Name)
		}
		req := enrollReq(res.Token)
		req.Hostname = hostname
		enr, err := svc.Enroll(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		a := mustGet(ctx, t, svc, enr.AgentID)
		if enr.AgentName != a.Name {
			t.Fatalf("enroll answered %q, host is named %q", enr.AgentName, a.Name)
		}
		return a
	}
	if a := enroll("web-1"); a.Name != "web-1" {
		t.Fatalf("name %q, want web-1", a.Name)
	}
	second := enroll("web-1")
	if second.Name != "web-1-2" {
		t.Fatalf("name %q, want web-1-2", second.Name)
	}
	if a := enroll("bad host;rm"); !strings.HasPrefix(a.Name, "new-host-") {
		t.Fatalf("an invalid hostname became the name %q", a.Name)
	}

	// A re-enroll keeps the name, even when the hostname changed.
	rot, err := svc.RotateEnrollToken(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := enrollReq(rot.Token)
	req.Hostname = "renamed"
	if _, err := svc.Enroll(ctx, req); err != nil {
		t.Fatal(err)
	}
	if a := mustGet(context.Background(), t, svc, second.ID); a.Name != "web-1-2" {
		t.Fatalf("re-enroll renamed the host to %q", a.Name)
	}
}

func TestValidateStacksDir(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"/srv/stacks", true},
		{"/var/lib/dockmesh-agent/stacks", true},
		{"/mnt/disk1/dockmesh-stacks.v2", true},
		{"", false},
		{"/", false},
		{"srv/stacks", false},
		{"/srv/../etc", false},
		{"/srv/stacks/", false},
		{"/srv//stacks", false},
		{"/srv/my stacks", false},
		{"/srv/$HOME", false},
		{"/" + strings.Repeat("a", maxStacksDirLen), false},
	}
	for _, tc := range tests {
		if err := ValidateStacksDir(tc.path); (err == nil) != tc.ok {
			t.Errorf("ValidateStacksDir(%q) = %v, want ok=%v", tc.path, err, tc.ok)
		}
	}
	svc, _ := newTestService(t)
	if _, err := svc.CreateWith(context.Background(), CreateOptions{Name: "x", StacksDir: "relative"}); !errors.Is(err, ErrInvalidStacksDir) {
		t.Fatalf("CreateWith with a relative stacks dir = %v", err)
	}
}

func TestSetStacksDir(t *testing.T) {
	ctx := context.Background()
	online := func(t *testing.T, svc *Service, hello HelloPayload) (*ConnectedAgent, string) {
		t.Helper()
		id, fp := createdHost(ctx, t, svc, CreateOptions{Name: "edge-1"})
		ag := newConnectedAgent(id, "edge-1", func() {})
		ag.certFP = fp
		ag.Hello = hello
		if err := svc.markOnline(ctx, ag, hello); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { svc.markOffline(ag) }) //nolint:contextcheck // markOffline has no caller context
		return ag, id
	}

	t.Run("current dir confirms the setup", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, id := online(t, svc, defaultDirHello())
		for range 2 {
			applied, err := svc.SetStacksDir(ctx, id, "/var/lib/dockmesh-agent/stacks")
			if err != nil || !applied {
				t.Fatalf("SetStacksDir = %v, %v; want applied", applied, err)
			}
			if a := mustGet(ctx, t, svc, id); a.NeedsSetup || a.StacksDir.Status != StacksDirStateApplied {
				t.Fatalf("needs_setup %v stacks_dir %+v", a.NeedsSetup, a.StacksDir)
			}
		}
	})

	t.Run("another dir goes to the agent", func(t *testing.T) {
		svc, _ := newTestService(t)
		ag, id := online(t, svc, defaultDirHello())
		applied, err := svc.SetStacksDir(ctx, id, "/srv/stacks")
		if err != nil || applied {
			t.Fatalf("SetStacksDir = %v, %v; want sent", applied, err)
		}
		f := nextFrame(t, ag)
		if a := mustGet(ctx, t, svc, id); !a.NeedsSetup || a.StacksDir.Status != StacksDirStatePending {
			t.Fatalf("while waiting: needs_setup %v stacks_dir %+v", a.NeedsSetup, a.StacksDir)
		}
		if _, err := svc.SetStacksDir(ctx, id, "/opt/stacks"); !errors.Is(err, ErrApplyRunning) {
			t.Fatalf("second request while the first runs = %v", err)
		}
		if _, err := svc.SetStacksDir(ctx, id, "/var/lib/dockmesh-agent/stacks"); !errors.Is(err, ErrApplyRunning) {
			t.Fatalf("confirming the current dir while a switch runs = %v", err)
		}
		payload, _ := json.Marshal(ResponseEnvelope{OK: false, Error: "unknown frame type"})
		ag.deliverResponse(Frame{Type: FrameRes, ID: f.ID, Payload: payload})
		deadline := time.Now().Add(5 * time.Second)
		for {
			a := mustGet(ctx, t, svc, id)
			if a.StacksDir.Status == StacksDirStateRefused {
				if a.StacksDir.Error != "unknown frame type" || !a.NeedsSetup {
					t.Fatalf("refused state %+v needs_setup %v", a.StacksDir, a.NeedsSetup)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("agent error not recorded: %+v", a.StacksDir)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		svc, _ := newTestService(t)
		_, id := online(t, svc, HelloPayload{Layout: LayoutLegacy, StacksDirs: []StacksDirInfo{{Path: "/var/lib/dockmesh/stacks", Current: true}}})
		if _, err := svc.SetStacksDir(ctx, id, "/srv/stacks"); !errors.Is(err, ErrLegacyLayout) {
			t.Fatalf("legacy agent = %v", err)
		}
		if _, err := svc.SetStacksDir(ctx, id, "srv"); !errors.Is(err, ErrInvalidStacksDir) {
			t.Fatalf("relative path = %v", err)
		}
		offline, _ := createdHost(ctx, t, svc, CreateOptions{Name: "edge-2"})
		if _, err := svc.SetStacksDir(ctx, offline, "/srv/stacks"); !errors.Is(err, ErrAgentNotConnected) {
			t.Fatalf("offline host = %v", err)
		}
	})
}

// A request for another directory decides the setup, even when the
// install set a stacks directory of its own.
func TestPendingRequestOutranksInstallDir(t *testing.T) {
	svc, _ := newTestService(t)
	id, fp := createdHost(context.Background(), t, svc, CreateOptions{Name: "edge-1", StacksDir: "/srv/stacks"})
	installDir := HelloPayload{Layout: LayoutManaged, StacksDirSet: true, StacksDirs: []StacksDirInfo{{Path: "/opt/stacks", Current: true}}}
	connectWith(t, svc, id, fp, installDir, &StacksDirApplyRes{Status: StacksDirPending})
	if a := mustGet(context.Background(), t, svc, id); !a.NeedsSetup {
		t.Fatal("the install's directory ended the setup while a request was pending")
	}
}

func TestLocalIsNeverTakenFromTheHostname(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.CreateWith(ctx, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req := enrollReq(res.Token)
	req.Hostname = "LOCAL"
	enr, err := svc.Enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if enr.AgentName != res.Agent.Name {
		t.Fatalf("host named %q, want the generated %q", enr.AgentName, res.Agent.Name)
	}
}

func TestCheckStacksDirChangesNothing(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	id, fp := createdHost(t, svc, CreateOptions{Name: "edge-1"})
	ag := newConnectedAgent(id, "edge-1", func() {})
	ag.certFP = fp
	ag.Hello = defaultDirHello()
	if err := svc.markOnline(ctx, ag, ag.Hello); err != nil {
		t.Fatal(err)
	}
	defer svc.markOffline(ag)
	type out struct {
		c   StacksDirCheck
		err error
	}
	done := make(chan out, 1)
	go func() { c, err := svc.CheckStacksDir(ctx, id, "/srv/stacks"); done <- out{c, err} }()
	f := nextFrame(t, ag)
	var req StacksDirApplyReq
	if err := json.Unmarshal(f.Payload, &req); err != nil || !req.CheckOnly || req.Path != "/srv/stacks" {
		t.Fatalf("request %+v (%v), want a check of /srv/stacks", req, err)
	}
	data, _ := json.Marshal(StacksDirApplyRes{Status: StacksDirRefused, Check: StacksDirCheck{Path: "/srv/stacks", Status: "fail", Message: strings.Repeat("x", 2000)}})
	payload, _ := json.Marshal(ResponseEnvelope{OK: true, Data: data})
	ag.deliverResponse(Frame{Type: FrameRes, ID: f.ID, Payload: payload})
	r := <-done
	if r.err != nil || r.c.Status != "fail" || len(r.c.Message) != maxStacksDirError {
		t.Fatalf("check = %+v (%d bytes), %v", r.c, len(r.c.Message), r.err)
	}
	if a := mustGet(t, svc, id); !a.NeedsSetup || a.StacksDir != nil {
		t.Fatalf("a check changed the host: needs_setup %v stacks_dir %+v", a.NeedsSetup, a.StacksDir)
	}
	if _, err := svc.CheckStacksDir(ctx, id, "../x"); !errors.Is(err, ErrInvalidStacksDir) {
		t.Fatalf("relative path = %v", err)
	}
}
