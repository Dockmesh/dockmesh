package updater

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/operations"
)

func newOps(t *testing.T) *operations.Service {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return operations.New(database)
}

func TestDrainPullReportsProgressWithoutSecrets(t *testing.T) {
	const pw = "registry-pass-31337"
	blob, _ := json.Marshal(map[string]string{"username": "ci", "password": pw})
	auth := base64.URLEncoding.EncodeToString(blob)

	ops := newOps(t)
	events, unsub := ops.Subscribe()
	defer unsub()
	op := ops.Start(context.Background(), operations.Spec{Kind: "image.update", Target: "web", Steps: UpdateSteps})
	op.AddSecrets(authSecrets(auth)...)
	op.StepStart(StepPull, "")
	stream := strings.Join([]string{
		`{"status":"Pulling from library/nginx","id":"1.27"}`,
		`{"status":"Downloading","id":"a1","progressDetail":{"current":50,"total":100}}`,
		`{"status":"Downloading","id":"b2","progressDetail":{"current":0,"total":100}}`,
		`{"status":"Pull complete","id":"a1"}`,
		`{"status":"Already exists","id":"b2"}`,
		`{"error":"unauthorized: login ci:` + pw + ` failed","errorDetail":{"message":"x"}}`,
		`{"status":"Digest: sha256:abc"}`,
		`{"status":"Status: Downloaded newer image for nginx:1.27, auth ` + auth + `"}`,
	}, "\n")
	if err := drainPull(strings.NewReader(stream), op, StepPull); err != nil {
		t.Fatal(err)
	}
	op.StepDone(StepPull, "")
	op.Finish(nil)

	got, err := ops.Get(context.Background(), op.ID())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range got.Log {
		lines = append(lines, l.Text)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"a1: Pull complete", "b2: Already exists", "Digest: sha256:abc"} {
		if !strings.Contains(all, want) {
			t.Errorf("log misses %q:\n%s", want, all)
		}
	}
	for _, leak := range []string{pw, auth} {
		if strings.Contains(all, leak) {
			t.Fatalf("stored log leaks %q:\n%s", leak, all)
		}
	}
	for {
		select {
		case ev := <-events:
			if ev.Line != nil && (strings.Contains(ev.Line.Text, pw) || strings.Contains(ev.Line.Text, auth)) {
				t.Fatalf("published line leaks: %q", ev.Line.Text)
			}
		default:
			return
		}
	}
}

func TestDrainPullWithoutOperationReadsToEnd(t *testing.T) {
	r := strings.NewReader(`{"status":"a"}` + "\n" + `not json` + "\n")
	if err := drainPull(r, nil, StepPull); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 0 {
		t.Fatalf("%d bytes left unread", r.Len())
	}
	r = strings.NewReader(`{"status":"a"}` + "\n" + `not json, more` + "\n")
	ops := newOps(t)
	op := ops.Start(context.Background(), operations.Spec{Kind: "image.update"})
	if err := drainPull(r, op, StepPull); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 0 {
		t.Fatalf("%d bytes left unread after a parse error", r.Len())
	}
}
