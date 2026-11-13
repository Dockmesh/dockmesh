package gitsource

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func intp(v int) *int { return &v }

func TestConfigurePollInterval(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	svc := New(database, nil, nil, t.TempDir(), nil)
	ctx := context.Background()

	steps := []struct {
		name string
		poll *int
		want int
		err  error
	}{
		{"create without interval", nil, defaultPollSec, nil},
		{"turn polling off", intp(0), 0, nil},
		{"update without interval keeps it", nil, 0, nil},
		{"set interval", intp(120), 120, nil},
		{"too short", intp(30), 120, ErrPollTooShort},
		{"negative", intp(-1), 120, ErrPollTooShort},
	}
	for _, st := range steps {
		_, err := svc.Configure(ctx, "web", Input{RepoURL: "https://example.com/r.git", PollIntervalSec: st.poll})
		if !errors.Is(err, st.err) {
			t.Fatalf("%s: err = %v, want %v", st.name, err, st.err)
		}
		got, err := svc.Get(ctx, "web")
		if err != nil {
			t.Fatal(err)
		}
		if got.PollIntervalSec != st.want {
			t.Errorf("%s: poll_interval_sec = %d, want %d", st.name, got.PollIntervalSec, st.want)
		}
	}
}
