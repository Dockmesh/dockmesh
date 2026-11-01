package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/operations"
)

func TestNewSiteOperationMasksOptionSecrets(t *testing.T) {
	const (
		header = "hdr-secret-value-99"
		pw     = "basic-auth-pw-77"
		query  = "fwd-token-55"
	)
	s := newServiceDB(t)
	ops := operations.New(s.db)
	events, unsub := ops.Subscribe()
	defer unsub()
	op := ops.Start(context.Background(), operations.Spec{Kind: "proxy.site", Target: "app.example.com", Steps: NewSiteSteps})
	opts := SiteOptions{
		Headers: HeaderOptions{RequestSet: []Header{{Name: "X-Token", Value: header}}},
		Access: AccessOptions{
			BasicAuth:   []BasicAuthUser{{Username: "u", Password: pw}},
			ForwardAuth: &ForwardAuth{URL: "http://auth:9091/verify?token=" + query},
		},
	}
	ctx := operations.WithOp(context.Background(), op)
	route, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.1:80", TLSMode: "internal", Options: &opts})
	if err != nil {
		t.Fatal(err)
	}
	op.Logf("info", "upstream answered for %s with %s / %s / %s", route.Host, header, pw, query)
	s.WatchCertificate(context.Background(), op, route.Host, route.TLSMode, time.Now())

	got, err := ops.Get(context.Background(), op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != operations.StatusSucceeded {
		t.Fatalf("status %s: %s", got.Status, got.Error)
	}
	want := []operations.StepStatus{operations.StepSucceeded, operations.StepSkipped, operations.StepSkipped}
	for i, st := range got.Steps {
		if st.Status != want[i] {
			t.Errorf("step %s: %s, want %s (%s)", st.Key, st.Status, want[i], st.Detail)
		}
	}
	check := func(where, text string) {
		for _, sec := range []string{header, pw, query} {
			if strings.Contains(text, sec) {
				t.Fatalf("%s leaks %q: %q", where, sec, text)
			}
		}
	}
	for _, l := range got.Log {
		check("stored log", l.Text)
	}
	for {
		select {
		case ev := <-events:
			if ev.Line != nil {
				check("published log", ev.Line.Text)
			}
			for _, st := range ev.Op.Steps {
				check("published step", st.Detail)
			}
		default:
			return
		}
	}
}

func TestCancelCertificateWait(t *testing.T) {
	s := newServiceDB(t)
	s.enabled.Store(true)
	ops := operations.New(s.db)
	op := ops.Start(context.Background(), operations.Spec{Kind: "proxy.site", Target: "app.invalid", Steps: NewSiteSteps})
	op.StepDone(StepSave, "")
	op.StepDone(StepApply, "")
	done := make(chan struct{})
	go func() {
		s.WatchCertificate(context.Background(), op, "app.invalid", "internal", time.Now())
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := ops.Get(context.Background(), op.ID())
		if got != nil && got.Cancellable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("certificate wait never became cancellable")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := ops.Cancel(op.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not stop")
	}
	got, err := ops.Get(context.Background(), op.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != operations.StatusCancelled || got.Steps[2].Status != operations.StepCancelled ||
		got.Steps[0].Status != operations.StepSucceeded {
		t.Fatalf("operation %s %q steps %+v", got.Status, got.Error, got.Steps)
	}
}
