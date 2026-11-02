package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/docker/api/types/container"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestIsOwnContainer(t *testing.T) {
	self := "aaaa000000000000000000000000000000000000000000000000000000000000"
	host.SetSelfContainerID(self)
	t.Cleanup(func() { host.SetSelfContainerID("") })
	inspect := func(id, image string, labels map[string]string) *container.InspectResponse {
		info := &container.InspectResponse{Config: &container.Config{Image: image, Labels: labels}}
		info.ContainerJSONBase = &container.ContainerJSONBase{ID: id}
		return info
	}
	tests := []struct {
		name   string
		hostID string
		info   *container.InspectResponse
		want   bool
	}{
		{"proxy", "local", inspect("p1", "caddy:2", map[string]string{"dockmesh.component": "proxy"}), true},
		{"helper on an agent", "agent-1", inspect("h1", "alpine", map[string]string{"dockmesh.component": "backup-helper"}), true},
		{"server by id", "local", inspect(self, "dockmesh:dev", nil), true},
		{"same id on an agent", "agent-1", inspect(self, "nginx", nil), false},
		{"agent image", "agent-1", inspect("a1", "ghcr.io/dockmesh/agent:0.5.0", nil), true},
		{"user container", "local", inspect("u1", "nginx", map[string]string{"com.docker.compose.project": "web"}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOwnContainer(tt.hostID, tt.info); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// The proxy's config and certificate volumes and an agent's volume are
// browsable by admins only, and non-admins can't back them up either.
func TestOwnVolumesNeedAdmin(t *testing.T) {
	e := newAuthzEnv(t)
	ids := map[string]string{"root": e.user("root", "admin").ID, "op": e.user("op", "operator").ID}
	tests := []struct {
		user, volume string
		want         bool
	}{
		{"op", "dockmesh-caddy-data", false},
		{"op", "dockmesh-caddy-config", false},
		{"op", "dockmesh-agent", false},
		{"op", "app-data", true},
		{"root", "dockmesh-caddy-data", true},
	}
	for _, tt := range tests {
		t.Run(tt.user+"/"+tt.volume, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			id := ids[tt.user]
			req = req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: id, Access: e.h.Roles.AccessFor(id)}))
			rr := httptest.NewRecorder()
			if got := e.h.requireVolumeNotOwn(rr, req, tt.volume); got != tt.want {
				t.Errorf("allowed = %v, want %v (%d)", got, tt.want, rr.Code)
			}
		})
	}
}

func TestOwnContainerGuardedPerms(t *testing.T) {
	for _, p := range []rbac.Perm{rbac.PermContainersUpdate, rbac.PermContainersDelete, rbac.PermContainersExec, rbac.PermContainersUpgrade, rbac.PermContainersFiles} {
		if !ownContainerGuarded(p) {
			t.Errorf("%s is not guarded", p)
		}
	}
	for _, p := range []rbac.Perm{rbac.PermContainersView, rbac.PermContainersLogs} {
		if ownContainerGuarded(p) {
			t.Errorf("%s is guarded", p)
		}
	}
}
