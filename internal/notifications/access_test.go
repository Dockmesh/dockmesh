package notifications

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type fakeUsers map[string]struct {
	role string
	tags []string
}

func (f fakeUsers) ListActiveUserIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(f))
	for id := range f {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f fakeUsers) UserAccess(_ context.Context, id string) (string, []string, error) {
	u, ok := f[id]
	if !ok {
		return "", nil, errors.New("no user")
	}
	return u.role, u.tags, nil
}

type fakeRoles struct {
	perms  map[string][]rbac.Perm
	scopes map[string][]rbac.Scope
}

func (f fakeRoles) AllowedDB(role string, perm rbac.Perm) bool {
	for _, p := range f.perms[role] {
		if p == perm {
			return true
		}
	}
	return false
}

func (f fakeRoles) InScope(role string, req rbac.ScopeRequest) bool {
	sc := f.scopes[role]
	if len(sc) == 0 {
		return true
	}
	for _, s := range sc {
		switch {
		case s.ScopeType == "host" && s.ScopeValue == req.HostID:
			return true
		case s.ScopeType == "stack" && s.ScopeValue == req.StackName:
			return true
		}
	}
	return false
}

type fakeTags map[string][]string

func (f fakeTags) Tags(id string) []string { return f[id] }

func TestEmitFanOutHonoursPermissionAndScope(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"admin", "viewer", "noperm", "hostA", "stackY", "tagged"} {
		if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES (?, ?, 'x', 'viewer')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	users := fakeUsers{
		"admin":  {role: "admin"},
		"viewer": {role: "viewer"},
		"noperm": {role: "none"},
		"hostA":  {role: "host-a"},
		"stackY": {role: "stack-y"},
		"tagged": {role: "viewer", tags: []string{"dev"}},
	}
	roles := fakeRoles{
		perms: map[string][]rbac.Perm{
			"admin": {rbac.PermStacksView}, "viewer": {rbac.PermStacksView}, "host-a": {rbac.PermStacksView}, "stack-y": {rbac.PermStacksView},
		},
		scopes: map[string][]rbac.Scope{
			"host-a":  {{ScopeType: "host", ScopeValue: "a"}},
			"stack-y": {{ScopeType: "stack", ScopeValue: "y"}},
		},
	}
	svc := NewService(database)
	svc.SetUserDirectory(users)
	svc.SetAccessChecker(&RBACAccess{Users: users, Roles: roles, Tags: fakeTags{"b": {"prod"}}})

	tests := []struct {
		name string
		in   EmitInput
		want []string
	}{
		{"no permission set", EmitInput{Kind: KindAlertFire, Title: "t1"}, nil},
		{"stack on host b", EmitInput{Kind: KindDeployOK, Title: "t2", Perm: rbac.PermStacksView, HostID: "b", StackName: "x"}, []string{"admin", "viewer"}},
		{"stack y on host b", EmitInput{Kind: KindDeployOK, Title: "t3", Perm: rbac.PermStacksView, HostID: "b", StackName: "y"}, []string{"admin", "viewer", "stackY"}},
		{"stack on host a", EmitInput{Kind: KindDeployOK, Title: "t4", Perm: rbac.PermStacksView, HostID: "a", StackName: "x"}, []string{"admin", "viewer", "hostA"}},
		{"hostless event", EmitInput{Kind: KindAlertFire, Title: "t5", Perm: rbac.PermStacksView}, []string{"admin", "viewer", "hostA", "stackY", "tagged"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Emit(context.Background(), tt.in); err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for id := range users {
				list, err := svc.List(context.Background(), id, false, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range list {
					if n.Title == tt.in.Title {
						got[id] = true
					}
				}
			}
			want := map[string]bool{}
			for _, id := range tt.want {
				want[id] = true
			}
			if len(got) != len(want) {
				t.Fatalf("recipients %v, want %v", got, want)
			}
			for id := range want {
				if !got[id] {
					t.Fatalf("recipients %v, want %v", got, want)
				}
			}
		})
	}
}
