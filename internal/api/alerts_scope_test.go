package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Alert rules are instance-wide: only assignments on everything see them.
// History follows the caller's host scope; entries without a host need
// history access on everything. A global grant next to a scoped one keeps
// its full view.
func TestAlertEndpointsFollowScope(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authSvc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
	roles := rbac.NewStore(database)
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}
	prev := middleware.RBACStore
	middleware.RBACStore = roles
	t.Cleanup(func() { middleware.RBACStore = prev })
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.New(handlers.Deps{Auth: authSvc, Roles: roles, Stacks: mgr,
		Deployments: stacks.NewDeploymentStore(database), Alerts: alerts.NewService(database, nil)})
	router := NewRouter(h, authSvc, nil, true, nil)

	if _, err := database.Exec(`INSERT INTO agents (id, name, status) VALUES ('agent-1', 'edge', 'online')`); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ host, name string }{{"local", "web"}, {"agent-1", "edge/db"}, {"", "nightly"}} {
		if _, err := database.Exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, host_id)
			VALUES (1, 'r', ?, 'fired', '', ?)`, e.name, e.host); err != nil {
			t.Fatal(err)
		}
	}

	user := func(name string, assignments ...rbac.Assignment) string {
		t.Helper()
		u, err := authSvc.CreateUser(ctx, name, "", "correct horse battery", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range assignments {
			a.UserID = u.ID
			if _, err := roles.AddAssignment(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if err := roles.Load(ctx); err != nil {
			t.Fatal(err)
		}
		res, err := authSvc.Login(ctx, name, "correct horse battery", "test", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return res.AccessToken
	}
	agent := rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"}
	tests := []struct {
		name       string
		token      string
		rules      int
		stats      int
		history    string
		createRule int
	}{
		{"global viewer", user("gv", rbac.Assignment{Role: rbac.RoleViewer, Scope: rbac.Global}),
			http.StatusOK, http.StatusOK, "edge/db,nightly,web", http.StatusForbidden},
		{"agent viewer", user("av", rbac.Assignment{Role: rbac.RoleViewer, Scope: agent}),
			http.StatusForbidden, http.StatusForbidden, "edge/db", http.StatusForbidden},
		{"agent host admin", user("aha", rbac.Assignment{Role: rbac.RoleHostAdmin, Scope: agent}),
			http.StatusForbidden, http.StatusForbidden, "edge/db", http.StatusForbidden},
		{"global admin with a scoped grant", user("mix",
			rbac.Assignment{Role: rbac.RoleAdmin, Scope: rbac.Global},
			rbac.Assignment{Role: rbac.RoleOperator, Scope: agent}),
			http.StatusOK, http.StatusOK, "edge/db,nightly,web", http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			do := func(method, path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+tt.token)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			rr := do(http.MethodGet, "/api/v1/alerts/rules", "")
			if rr.Code != tt.rules {
				t.Fatalf("rules: %d %s, want %d", rr.Code, rr.Body, tt.rules)
			}
			if rr.Code == http.StatusOK {
				var list []alerts.Rule
				if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				builtin := 0
				for _, r := range list {
					if r.Builtin {
						builtin++
					}
				}
				if builtin == 0 {
					t.Error("built-in rules hidden from a global viewer")
				}
			}
			if code := do(http.MethodGet, "/api/v1/alerts/rules/1/stats", "").Code; code != tt.stats {
				t.Errorf("stats: %d, want %d", code, tt.stats)
			}

			rr = do(http.MethodGet, "/api/v1/alerts/history", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("history: %d %s", rr.Code, rr.Body)
			}
			var hist []alerts.HistoryEntry
			if err := json.Unmarshal(rr.Body.Bytes(), &hist); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range hist {
				names = append(names, e.ContainerName)
			}
			sort.Strings(names)
			want := tt.history
			if got := strings.Join(names, ","); got != want {
				t.Errorf("history %q, want %q", got, want)
			}

			rule := `{"name":"oom-` + strings.ReplaceAll(tt.name, " ", "-") + `","kind":"oom","container_filter":"*"}`
			if code := do(http.MethodPost, "/api/v1/alerts/rules", rule).Code; code != tt.createRule {
				t.Errorf("create rule: %d, want %d", code, tt.createRule)
			}
		})
	}
}
