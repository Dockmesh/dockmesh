package notifications

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type fakeUsers map[string]*rbac.Access

func (f fakeUsers) ListActiveUserIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(f))
	for id := range f {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f fakeUsers) AccessFor(id string) *rbac.Access {
	if a, ok := f[id]; ok {
		return a
	}
	return &rbac.Access{}
}

func grant(perm rbac.Perm, scopes ...rbac.Scope) *rbac.Access {
	return &rbac.Access{Authenticated: true, Grants: []rbac.Grant{{Perms: map[rbac.Perm]bool{perm: true}, Scopes: scopes}}}
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
		if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES (?, ?, 'x')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	users := fakeUsers{
		"admin":  grant(rbac.PermStacksView),
		"viewer": grant(rbac.PermStacksView),
		"noperm": {Authenticated: true},
		"hostA":  grant(rbac.PermStacksView, rbac.Scope{Type: rbac.ScopeHost, Host: "a"}),
		"stackY": grant(rbac.PermStacksView, rbac.Scope{Type: rbac.ScopeStack, Value: "y"}),
		"tagged": grant(rbac.PermStacksView, rbac.Scope{Type: rbac.ScopeHostTag, Value: "dev"}),
	}
	svc := NewService(database)
	svc.SetUserDirectory(users)
	svc.SetAccessChecker(&RBACAccess{Access: users, Tags: fakeTags{"b": {"prod"}}})

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
		{"unscoped only", EmitInput{Kind: KindBackupFail, Title: "t6", Perm: rbac.PermStacksView, HostID: "a", Unscoped: true}, []string{"admin", "viewer"}},
		{"unscoped needs the permission", EmitInput{Kind: KindBackupFail, Title: "t7", Perm: rbac.PermSystemUpdate, Unscoped: true}, nil},
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
