package scaling

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

const validRules = `{"scaling":{"enabled":true,"rules":[{"service":"web","min_replicas":1,"max_replicas":2,"scale_up":{"metric":"cpu","threshold_percent":80}}]}}`

func scalingEnv(t *testing.T) (*stacks.Manager, *notices.Service, string) {
	t.Helper()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	m, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.SetSumStore(stacks.NewSumStore(database))
	n := notices.NewService(database)
	m.SetNotices(n)
	if _, err := m.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
		t.Fatal(err)
	}
	dir, err := m.Dir("app")
	if err != nil {
		t.Fatal(err)
	}
	return m, n, dir
}

// saveMeta writes the meta file the way the API does, so its checksum is
// recorded.
func saveMeta(t *testing.T, m *stacks.Manager, dir, content string) {
	t.Helper()
	if err := m.WriteFile("app", stacks.MetaFileName, func() error {
		return os.WriteFile(filepath.Join(dir, stacks.MetaFileName), []byte(content), 0o644)
	}); err != nil {
		t.Fatal(err)
	}
}

func outside(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, m *stacks.Manager, dir string)
		wantRules  bool
		wantReason string
	}{
		{"no meta file", func(*testing.T, *stacks.Manager, string) {}, false, ""},
		{"rules saved through the API", func(t *testing.T, m *stacks.Manager, dir string) { saveMeta(t, m, dir, validRules) }, true, ""},
		{"rules disabled", func(t *testing.T, m *stacks.Manager, dir string) {
			saveMeta(t, m, dir, `{"scaling":{"enabled":false,"rules":[{"service":"web","max_replicas":2}]}}`)
		}, false, ""},
		{"rules written outside", func(t *testing.T, _ *stacks.Manager, dir string) { outside(t, dir, stacks.MetaFileName, validRules) }, false, stacks.PauseModifiedOutside},
		{"compose changed outside", func(t *testing.T, m *stacks.Manager, dir string) {
			saveMeta(t, m, dir, validRules)
			outside(t, dir, "compose.yaml", "services: {web: {image: x, privileged: true}}\n")
		}, false, stacks.PauseModifiedOutside},
		{"override file added outside", func(t *testing.T, m *stacks.Manager, dir string) {
			saveMeta(t, m, dir, validRules)
			outside(t, dir, "compose.x.yaml", "services: {web: {privileged: true}}\n")
		}, false, stacks.PauseModifiedOutside},
		{"invalid rule", func(t *testing.T, m *stacks.Manager, dir string) {
			saveMeta(t, m, dir, `{"scaling":{"enabled":true,"rules":[{"service":"web","min_replicas":0,"max_replicas":0}]}}`)
		}, false, stacks.PauseInvalidRules},
		{"unparsable meta file", func(t *testing.T, m *stacks.Manager, dir string) { saveMeta(t, m, dir, `{`) }, false, stacks.PauseInvalidRules},
		{"default override not deployed", func(t *testing.T, m *stacks.Manager, dir string) {
			outside(t, dir, "compose.prod.yaml", "services: {}\n")
			if err := m.AcceptExternal(context.Background(), "app"); err != nil {
				t.Fatal(err)
			}
			saveMeta(t, m, dir, `{"active_environment":"prod",`+validRules[1:])
		}, false, stacks.PauseOverrideUnconfirmed},
		{"default override deployed", func(t *testing.T, m *stacks.Manager, dir string) {
			outside(t, dir, "compose.prod.yaml", "services: {}\n")
			if err := m.AcceptExternal(context.Background(), "app"); err != nil {
				t.Fatal(err)
			}
			saveMeta(t, m, dir, `{"active_environment":"prod",`+validRules[1:])
			if err := m.RecordOverride("app", "prod"); err != nil {
				t.Fatal(err)
			}
		}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, dir := scalingEnv(t)
			tt.setup(t, m, dir)
			v, err := Check(m, "app")
			if err != nil {
				t.Fatal(err)
			}
			if (v.Rules != nil) != tt.wantRules {
				t.Errorf("rules = %+v, want rules %v", v.Rules, tt.wantRules)
			}
			got := ""
			if v.Pause != nil {
				got = v.Pause.Reason
			}
			if got != tt.wantReason {
				t.Errorf("pause = %+v, want reason %q", v.Pause, tt.wantReason)
			}
			if v.Rules != nil && (v.Files == nil || v.Files.Detail.Compose != "services: {web: {image: x}}\n") {
				t.Errorf("verdict files = %+v", v.Files)
			}
		})
	}
}

func TestControllerSkipsPausedStack(t *testing.T) {
	ctx := context.Background()
	m, n, dir := scalingEnv(t)
	saveMeta(t, m, dir, validRules)
	c := NewController(m, nil, noTarget, func(context.Context, string, Target, int, int) error {
		t.Error("scale called")
		return nil
	})
	key := stacks.ScalerHeldNoticeKey("app")
	evaluated := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.states["app/web"]
		delete(c.states, "app/web")
		return ok
	}
	openCount := func() int {
		open, err := n.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		k := 0
		for _, o := range open {
			if o.Key == key {
				k++
			}
		}
		return k
	}

	c.tick(ctx)
	if !evaluated() || openCount() != 0 {
		t.Fatal("clean stack: rule not evaluated or notice raised")
	}

	outside(t, dir, "compose.yaml", "services: {web: {image: x, privileged: true}}\n")
	for range 3 {
		c.tick(ctx)
	}
	if evaluated() {
		t.Error("rule evaluated for a stack changed outside Dockmesh")
	}
	if got := openCount(); got != 1 {
		t.Errorf("open notices = %d, want 1", got)
	}

	if err := m.AcceptExternal(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	c.tick(ctx)
	if !evaluated() || openCount() != 0 {
		t.Error("confirmed stack: rule not evaluated or notice still open")
	}
}

func TestControllerResolvesNoticeFromEarlierStart(t *testing.T) {
	ctx := context.Background()
	m, n, dir := scalingEnv(t)
	saveMeta(t, m, dir, validRules)
	m.HoldScaler(ctx, "app", stacks.ScalerPause{Reason: stacks.PauseModifiedOutside, Files: []string{"compose.yaml"}})
	NewController(m, nil, noTarget, nil).tick(ctx)
	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range open {
		if o.Key == stacks.ScalerHeldNoticeKey("app") {
			t.Fatal("notice from an earlier start still open")
		}
	}
}

func noTarget(context.Context, string, string) (Target, error) {
	return nil, errors.New("no target")
}
