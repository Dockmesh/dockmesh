package notifications

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// AccessChecker decides which users a fan-out notification reaches.
type AccessChecker interface {
	CanReceive(ctx context.Context, userID string, in EmitInput) bool
}

// UserAccessSource looks up what limits a user: role and host-tag scope.
type UserAccessSource interface {
	UserAccess(ctx context.Context, userID string) (role string, scopeTags []string, err error)
}

type roleChecker interface {
	AllowedDB(role string, perm rbac.Perm) bool
	InScope(role string, req rbac.ScopeRequest) bool
}

type hostTagSource interface {
	Tags(hostID string) []string
}

// RBACAccess applies the same permission and scope rules the API does.
type RBACAccess struct {
	Users UserAccessSource
	Roles roleChecker
	Tags  hostTagSource
}

// CanReceive requires the user's role to hold in.Perm and, when the event
// names a host or stack, the user's tag scope and role scope to cover it.
// An event without a permission reaches nobody.
func (a *RBACAccess) CanReceive(ctx context.Context, userID string, in EmitInput) bool {
	if in.Perm == "" || a.Users == nil || a.Roles == nil {
		return false
	}
	role, scopeTags, err := a.Users.UserAccess(ctx, userID)
	if err != nil || !a.Roles.AllowedDB(role, in.Perm) {
		return false
	}
	if in.Unscoped {
		// An empty request matches only a role without scope rows.
		return len(scopeTags) == 0 && a.Roles.InScope(role, rbac.ScopeRequest{})
	}
	if in.HostID == "" && in.StackName == "" {
		return true
	}
	req := rbac.ScopeRequest{HostID: in.HostID, StackName: in.StackName}
	if in.HostID != "" && a.Tags != nil {
		req.HostTags = a.Tags.Tags(in.HostID)
	}
	if len(scopeTags) > 0 && in.HostID != "" && !rbac.ScopeMatchesHost(scopeTags, req.HostTags) {
		return false
	}
	return a.Roles.InScope(role, req)
}
