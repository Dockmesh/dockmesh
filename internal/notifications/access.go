package notifications

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// AccessChecker decides which users a fan-out notification reaches.
type AccessChecker interface {
	CanReceive(ctx context.Context, userID string, in EmitInput) bool
}

// AccessSource evaluates a user's current assignments.
type AccessSource interface {
	AccessFor(userID string) *rbac.Access
}

type hostTagSource interface {
	Tags(hostID string) []string
}

// RBACAccess applies the same permission and scope rules the API does.
type RBACAccess struct {
	Access AccessSource
	Tags   hostTagSource
}

// CanReceive requires the user to hold in.Perm on the host or stack the
// event names; an unscoped event needs the permission on everything. An
// event without a permission reaches nobody.
func (a *RBACAccess) CanReceive(ctx context.Context, userID string, in EmitInput) bool {
	if in.Perm == "" || a.Access == nil {
		return false
	}
	acc := a.Access.AccessFor(userID)
	if in.Unscoped {
		return acc.AllowsGlobally(in.Perm)
	}
	if in.HostID == "" && in.StackName == "" {
		return acc.Allows(in.Perm)
	}
	res := rbac.Resource{HostID: in.HostID, StackName: in.StackName}
	if in.HostID != "" && a.Tags != nil {
		res.HostTags = a.Tags.Tags(in.HostID)
	}
	return acc.AllowsOn(in.Perm, res)
}
