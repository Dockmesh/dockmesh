package updater

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/operations"
)

func newOpsDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

// A pull that hangs on the registry stops when the user cancels it; the
// operation is only cancellable while the pull runs.
func TestCancelPull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/create") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Pulling from library/web","id":"latest"}` + "\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	ops := operations.New(newOpsDB(t))
	op := ops.Start(context.Background(), operations.Spec{Kind: operations.KindImageUpdate, Target: "web", Steps: UpdateSteps})
	op.StepStart(StepPull, "")
	s := &Service{}
	done := make(chan error, 1)
	go func() { done <- s.pull(operations.WithOp(context.Background(), op), cli, "web:latest", "", nil) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		got, _ := ops.Get(context.Background(), op.ID())
		if got != nil && got.Cancellable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pull never became cancellable")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := ops.Cancel(op.ID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pull did not stop")
	}
	if !errors.Is(err, operations.ErrCancelled) {
		t.Fatalf("pull: %v, want cancelled", err)
	}
	if got, _ := ops.Get(context.Background(), op.ID()); got.Cancellable {
		t.Fatal("still cancellable after the pull ended")
	}
	op.StepFail(StepPull, err)
	op.Finish(err)
	got, _ := ops.Get(context.Background(), op.ID())
	if got.Status != operations.StatusCancelled || got.Steps[0].Status != operations.StepCancelled || got.Steps[2].Status != operations.StepSkipped {
		t.Fatalf("operation %s steps %+v", got.Status, got.Steps)
	}
}
