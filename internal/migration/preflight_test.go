package migration

import (
	"context"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func TestArchCheck(t *testing.T) {
	amd := &agents.DaemonInfo{OS: "linux", Arch: "amd64"}
	arm := &agents.DaemonInfo{OS: "linux", Arch: "arm64"}
	cases := []struct {
		name     string
		src, tgt *agents.DaemonInfo
		want     string
	}{
		{"same platform", amd, &agents.DaemonInfo{OS: "linux", Arch: "amd64"}, CheckPass},
		{"different arch", amd, arm, CheckFail},
		{"old agent on target", amd, nil, CheckUnknown},
		{"old agent on source", nil, arm, CheckUnknown},
		{"both unknown", nil, nil, CheckUnknown},
		{"report without arch", amd, &agents.DaemonInfo{OS: "linux"}, CheckUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, detail := archCheck(c.src, c.tgt); got != c.want {
				t.Fatalf("archCheck = %s (%s), want %s", got, detail, c.want)
			}
		})
	}
}

func TestPreflightArchMatch(t *testing.T) {
	amd := &agents.DaemonInfo{OS: "linux", Arch: "amd64"}
	cases := []struct {
		name       string
		src, tgt   *agents.DaemonInfo
		wantStatus string
		wantPassed bool
	}{
		{"known and equal", amd, amd, CheckPass, true},
		{"known and different", amd, &agents.DaemonInfo{OS: "linux", Arch: "arm64"}, CheckFail, false},
		{"older target agent doesn't block", amd, nil, CheckUnknown, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newMigrationEnv(t)
			e.source.daemon, e.target.daemon = c.src, c.tgt
			res, err := e.svc.Preflight(context.Background(), "web", "agent1")
			if err != nil {
				t.Fatal(err)
			}
			var got *PreflightCheck
			for i := range res.Checks {
				if res.Checks[i].Name == "arch_match" {
					got = &res.Checks[i]
				}
			}
			if got == nil || got.Status != c.wantStatus || res.Passed != c.wantPassed {
				t.Fatalf("arch_match = %+v, passed = %v; want %s, passed %v", got, res.Passed, c.wantStatus, c.wantPassed)
			}
		})
	}
}
