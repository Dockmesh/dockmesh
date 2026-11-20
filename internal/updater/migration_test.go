package updater

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/client"
)

// stackContainerDocker answers the inspect of container web of stack app
// and records every other call.
type stackContainerDocker struct {
	mu    sync.Mutex
	calls []string
}

func (f *stackContainerDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := apiVersion.ReplaceAllString(r.URL.Path, "")
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && path == "/containers/web/json" {
		_, _ = w.Write([]byte(`{"Id":"web","Name":"/app-web-1","Image":"sha256:old","State":{"Running":true},"HostConfig":{},` +
			`"Config":{"Image":"nginx:1","Labels":{"com.docker.compose.project":"app","com.docker.compose.service":"web"}}}`))
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+path)
	f.mu.Unlock()
	http.Error(w, `{"message":"not faked"}`, http.StatusInternalServerError)
}

func TestUpdateRefusesActiveMigration(t *testing.T) {
	tests := []struct {
		name    string
		status  string // "" = no migration
		refused bool
	}{
		{"no migration", "", false},
		{"migration running", migration.StatusSyncing, true},
		{"rollback running", migration.StatusRollingBack, true},
		{"failed migration", migration.StatusFailed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			if _, err := m.Create("app", "services:\n  web:\n    image: nginx:1\n", ""); err != nil {
				t.Fatal(err)
			}
			store := migration.NewStore(database)
			if tt.status != "" {
				now := time.Now()
				if err := store.Create(context.Background(), &migration.Migration{ID: "m1", StackName: "app",
					SourceHostID: "local", TargetHostID: "agent-1", Status: tt.status, StartedAt: &now, InitiatedBy: "u"}); err != nil {
					t.Fatal(err)
				}
			}
			fake := &stackContainerDocker{}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.43"))
			if err != nil {
				t.Fatal(err)
			}
			locks := stacklock.New()
			s := NewService(docker.Wrap(cli), database)
			s.SetLocks(locks)
			s.SetStacks(m, nil)
			s.SetMigrating(store.HasActive)

			_, err = s.Update(context.Background(), "web", "u")
			if got := errors.Is(err, migration.ErrMigrationActive); got != tt.refused {
				t.Fatalf("err = %v, refused = %v, want %v", err, got, tt.refused)
			}
			fake.mu.Lock()
			calls := fake.calls
			fake.mu.Unlock()
			if tt.refused && len(calls) > 0 {
				t.Errorf("refused update still called docker: %v", calls)
			}
			if !tt.refused && len(calls) == 0 {
				t.Error("update stopped before the pull")
			}
			release, err := locks.TryLock(stacklock.HostKey("local", "app"), stacklock.OpDeploy)
			if err != nil {
				t.Fatalf("stack lock kept: %v", err)
			}
			release()
		})
	}
}
