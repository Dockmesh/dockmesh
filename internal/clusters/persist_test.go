package clusters

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"
)

type statusRow struct {
	ClusterID, State, CheckedAt string
	Nodes, KnownAt              sql.NullString
}

func statusRows(t *testing.T, db *sql.DB) []statusRow {
	t.Helper()
	rows, err := db.Query(`SELECT cluster_id, state, CAST(checked_at AS TEXT), nodes, CAST(known_at AS TEXT) FROM cluster_status ORDER BY cluster_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []statusRow
	for rows.Next() {
		var r statusRow
		if err := rows.Scan(&r.ClusterID, &r.State, &r.CheckedAt, &r.Nodes, &r.KnownAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The last known state survives a restart with its times, and starting
// again changes nothing that is stored.
func TestStatusSurvivesRestart(t *testing.T) {
	cases := []struct {
		name string
		// after runs between create and the reconcile.
		after     func(f *fakeHosts)
		state     string
		listFresh bool
	}{
		{"healthy", func(*fakeHosts) {}, StateHealthy, true},
		{"degraded", func(f *fakeHosts) {
			n := readyNodes()
			n[3].State = "down"
			f.nodes["m1"] = n
		}, StateDegraded, true},
		{"origin offline", func(f *fakeHosts) { f.failing["m1"] = true }, StateUnreachable, false},
		{"no quorum", func(f *fakeHosts) { f.infos["m1"] = swarmInfo("", "n1", RoleManager) }, StateNoQuorum, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := lab()
			f.nodes["m1"], f.nodes["m2"] = readyNodes(), readyNodes()
			s := newService(t, f)
			ctx := context.Background()
			if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
				t.Fatal(err)
			}
			created := onlyCluster(t, s).Status
			f.mu.Lock()
			tc.after(f)
			f.mu.Unlock()
			// Keeps checked_at apart from the create's known_at.
			time.Sleep(5 * time.Millisecond)
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			want := onlyCluster(t, s).Status
			if want.State != tc.state || want.CheckedAt == nil || want.KnownAt == nil {
				t.Fatalf("before restart: %+v", want)
			}
			if fresh := want.KnownAt.Equal(*want.CheckedAt); fresh != tc.listFresh {
				t.Fatalf("node list fresh = %v, want %v", fresh, tc.listFresh)
			}
			if !tc.listFresh && !want.KnownAt.Equal(*created.KnownAt) {
				t.Fatalf("known_at %v, want the create's %v", want.KnownAt, created.KnownAt)
			}
			wantNodes, wantKnown, _ := s.LastNodes(swarmID)
			rows := statusRows(t, s.db)
			if len(rows) != 1 {
				t.Fatalf("stored rows = %+v", rows)
			}

			for start := 1; start <= 2; start++ {
				r := New(s.db, f)
				got := onlyCluster(t, r).Status
				if got.State != want.State || got.Managers != want.Managers || got.ManagersReachable != want.ManagersReachable ||
					got.Workers != want.Workers || got.NodesDown != want.NodesDown || got.NodesWithoutAgent != want.NodesWithoutAgent ||
					got.Leader != want.Leader {
					t.Fatalf("start %d: status = %+v, want %+v", start, got, want)
				}
				if got.CheckedAt == nil || !got.CheckedAt.Equal(*want.CheckedAt) || got.KnownAt == nil || !got.KnownAt.Equal(*want.KnownAt) {
					t.Fatalf("start %d: times = %v %v, want %v %v", start, got.CheckedAt, got.KnownAt, want.CheckedAt, want.KnownAt)
				}
				nodes, known, ok := r.LastNodes(swarmID)
				if !ok || !known.Equal(wantKnown) || !slices.Equal(nodes, wantNodes) {
					t.Fatalf("start %d: last nodes = %v %v %v", start, nodes, known, ok)
				}
				ms, err := r.Memberships(ctx)
				if err != nil || len(ms) != 3 {
					t.Fatalf("start %d: memberships = %v %v", start, ms, err)
				}
				for _, m := range ms {
					if m.Node != nil {
						t.Fatalf("start %d: node state before the first reconcile: %+v", start, m)
					}
				}
				if after := statusRows(t, s.db); !slices.Equal(after, rows) {
					t.Fatalf("start %d changed the stored rows:\n%+v\nwant\n%+v", start, after, rows)
				}
			}
		})
	}
}

// An unchanged node list isn't written again; known_at still moves.
func TestStatusUnchangedListNotRewritten(t *testing.T) {
	f := lab()
	f.nodes["m1"] = readyNodes()
	s := newService(t, f)
	ctx := context.Background()
	if _, err := s.Create(ctx, "m1", "lab", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE cluster_status SET nodes = '[]'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	rows := statusRows(t, s.db)
	if len(rows) != 1 || rows[0].Nodes.String != "[]" || rows[0].KnownAt.String != rows[0].CheckedAt {
		t.Fatalf("rows = %+v", rows)
	}
}
