//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/rootupdate"
)

type fakeUnitService struct {
	unit       string
	written    string
	writeErr   error
	restartErr error
	// healthy answers for the unit that is in place.
	healthy  map[string]bool
	restarts int
	calls    []string
}

func (f *fakeUnitService) ReadUnit() ([]byte, error) { return []byte(f.unit), nil }
func (f *fakeUnitService) RestoreUnit(b []byte) error {
	f.calls = append(f.calls, "restore")
	f.unit = string(b)
	return nil
}
func (f *fakeUnitService) DaemonReload(context.Context) error {
	f.calls = append(f.calls, "reload")
	return nil
}
func (f *fakeUnitService) WriteUnit(context.Context) error {
	f.calls = append(f.calls, "write")
	if f.writeErr != nil {
		return f.writeErr
	}
	f.unit = f.written
	return nil
}
func (f *fakeUnitService) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	f.restarts++
	return f.restartErr
}
func (f *fakeUnitService) Healthy(context.Context) bool { return f.healthy[f.unit] }

func newUnitRewriter(t *testing.T, svc unitService) *unitRewriter {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(rootupdate.Request{Type: rootupdate.RequestUnitRewrite})
	if err := os.WriteFile(filepath.Join(dir, rootupdate.RequestFile), req, 0o640); err != nil {
		t.Fatal(err)
	}
	return &unitRewriter{reqDir: dir, uid: uint32(os.Getuid()), svc: svc, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateDir: t.TempDir(), healthTimeout: 50 * time.Millisecond, poll: 5 * time.Millisecond,
		lock: func() (func(), error) { return func() {}, nil }}
}

func readUnitResult(t *testing.T, u *unitRewriter) rootupdate.UnitResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(u.reqDir, rootupdate.UnitResultFile))
	if err != nil {
		t.Fatal(err)
	}
	var r rootupdate.UnitResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnitRewriter(t *testing.T) {
	tests := []struct {
		name     string
		svc      *fakeUnitService
		pending  bool
		journal  bool
		status   string
		unit     string
		restarts int
		reason   string
	}{
		{name: "new unit comes up", svc: &fakeUnitService{unit: "old", written: "new", healthy: map[string]bool{"new": true}},
			status: rootupdate.UnitDone, unit: "new", restarts: 1},
		{name: "refused value keeps the unit and the service", svc: &fakeUnitService{unit: "old", writeErr: errors.New(`writable path "/etc/x" lies in /etc`)},
			status: rootupdate.UnitRefused, unit: "old", reason: "/etc/x"},
		{name: "unhealthy with the new unit rolls back", svc: &fakeUnitService{unit: "old", written: "new", healthy: map[string]bool{"old": true}},
			status: rootupdate.UnitRolledBack, unit: "old", restarts: 2, reason: "healthy"},
		{name: "restart fails rolls back", svc: &fakeUnitService{unit: "old", written: "new", restartErr: errors.New("boom"), healthy: map[string]bool{"old": true}},
			status: rootupdate.UnitFailed, unit: "old", restarts: 2, reason: "boom"},
		{name: "old unit does not come up either", svc: &fakeUnitService{unit: "old", written: "new", healthy: map[string]bool{}},
			status: rootupdate.UnitFailed, unit: "old", restarts: 2, reason: "rollback"},
		{name: "unfinished update journal", journal: true, svc: &fakeUnitService{unit: "old", written: "new"},
			status: rootupdate.UnitRefused, unit: "old", reason: "update"},
		{name: "update in flight", pending: true, svc: &fakeUnitService{unit: "old", written: "new"},
			status: rootupdate.UnitRefused, unit: "old", reason: "update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUnitRewriter(t, tt.svc)
			if tt.pending {
				if err := os.WriteFile(filepath.Join(u.reqDir, rootupdate.PendingFile), []byte("{}"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if tt.journal {
				if err := os.WriteFile(filepath.Join(u.stateDir, "journal.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := u.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			res := readUnitResult(t, u)
			if res.Status != tt.status || !strings.Contains(res.Reason, tt.reason) {
				t.Fatalf("result %+v, want %s with %q", res, tt.status, tt.reason)
			}
			if tt.svc.unit != tt.unit || tt.svc.restarts != tt.restarts {
				t.Fatalf("unit %q after %d restarts (%v), want %q after %d", tt.svc.unit, tt.svc.restarts, tt.svc.calls, tt.unit, tt.restarts)
			}
			if _, err := os.Stat(filepath.Join(u.reqDir, rootupdate.RequestFile)); !os.IsNotExist(err) {
				t.Fatal("request left for the path unit")
			}
		})
	}
}

func TestUnitRewriterWritesRestartingFirst(t *testing.T) {
	svc := &fakeUnitService{unit: "old", written: "new", healthy: map[string]bool{"new": true}}
	u := newUnitRewriter(t, svc)
	var seen string
	u.svc = restartHook{svc, func() { seen = readUnitResult(t, u).Status }}
	if err := u.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != rootupdate.UnitRestarting {
		t.Fatalf("result before the restart %q, want %q", seen, rootupdate.UnitRestarting)
	}
}

type restartHook struct {
	*fakeUnitService
	before func()
}

func (r restartHook) Restart(ctx context.Context) error {
	r.before()
	return r.fakeUnitService.Restart(ctx)
}

func TestRequestTypeUnitRewrite(t *testing.T) {
	u := newUnitRewriter(t, &fakeUnitService{})
	if got := requestType(u.reqDir, u.uid); got != rootupdate.RequestUnitRewrite {
		t.Fatalf("requestType = %q", got)
	}
}
