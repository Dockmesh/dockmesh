package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestStackRequestsStayOnTheStacksHost(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	deps := stacks.NewDeploymentStore(database)
	h := &Handlers{Deployments: deps, Hosts: host.NewRegistry(nil, nil)}
	for name, row := range map[string][2]string{
		"on-agent":   {"agent-1", "deployed"},
		"stopped":    {"agent-1", "stopped"},
		"moving":     {"agent-1", "migrating"},
		"moved-away": {"agent-1", "migrated_away"},
		"on-local":   {"local", "deployed"},
	} {
		if err := deps.Set(ctx, name, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := deps.SetTarget(ctx, "created-for-agent", "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	local, _ := h.Hosts.Pick("local")
	agent := host.NewRemote("agent-1", "dm-agent1", nil)

	pickCases := []struct {
		name, stack, query string
		wantHost           string
		wantErr            error
	}{
		{"no record goes to local", "new", "", "local", nil},
		{"agent stack without ?host= stays on its agent", "on-agent", "", "", host.ErrUnknownHost},
		{"stack created for an agent stays on it", "created-for-agent", "", "", host.ErrUnknownHost},
		{"explicit host wins", "on-agent", "?host=local", "local", nil},
		{"local stack", "on-local", "", "local", nil},
	}
	for _, tc := range pickCases {
		t.Run("pick/"+tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/stacks/"+tc.stack+"/deploy"+tc.query, nil)
			got, err := h.pickStackHost(r, tc.stack)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID() != tc.wantHost {
				t.Fatalf("got %v, %v; want %s", got, err, tc.wantHost)
			}
		})
	}

	refuseCases := []struct {
		name, stack string
		target      host.Host
		refused     bool
	}{
		{"deployed elsewhere", "on-agent", local, true},
		{"stopped elsewhere", "stopped", local, true},
		{"migrating", "moving", local, true},
		{"migrated away", "moved-away", local, false},
		{"own host", "on-agent", agent, false},
		{"no record", "new", local, false},
		{"local stack to an agent", "on-local", agent, true},
	}
	for _, tc := range refuseCases {
		t.Run("refuse/"+tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/stacks/"+tc.stack+"/deploy", nil)
			if got := h.refuseOtherHost(w, r, tc.stack, tc.target); got != tc.refused {
				t.Fatalf("refused = %v, want %v", got, tc.refused)
			}
			if tc.refused && w.Code != 409 {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			// No caller in the request, so the stack's host is not visible.
			if tc.refused && strings.Contains(w.Body.String(), "host_id") {
				t.Fatalf("409 names a host the caller can't see: %s", w.Body)
			}
		})
	}
}
