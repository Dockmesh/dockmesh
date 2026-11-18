package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/system"
)

func TestStacksDirDisks(t *testing.T) {
	const gib = 1 << 30
	mounts := []system.Mount{
		{MountPoint: "/", FSType: "ext4", Total: 30 * gib, Used: 9 * gib},
		{MountPoint: "/srv", FSType: "xfs", Total: 100 * gib, Used: 10 * gib},
		{MountPoint: "/mnt/disk1", FSType: "btrfs", Total: 50 * gib},
		{MountPoint: "/volume1", FSType: "ext4", Total: 10 * gib},
		{MountPoint: "/mnt/media", FSType: "nfs4", Total: 10 * gib},
		{MountPoint: "/mnt/usb", FSType: "exfat", Total: 10 * gib},
		{MountPoint: "/var/lib/docker", FSType: "ext4", Total: 10 * gib},
		{MountPoint: "/boot/efi", FSType: "vfat", Total: gib},
		{MountPoint: "/run", FSType: "tmpfs", Total: gib},
		{MountPoint: "/srv", FSType: "xfs"},
		{MountPoint: "/broken", FSType: "ext4", Error: "permission denied"},
	}
	type want struct {
		suggested string
		usable    bool
		reason    string
	}
	wants := map[string]want{
		"/":          {"/data/agent/stacks", true, ""},
		"/srv":       {"/srv/stacks", true, ""},
		"/mnt/disk1": {"/mnt/disk1/stacks", true, ""},
		"/volume1":   {"/volume1/stacks", false, "not allowed as a stacks dir"},
		"/mnt/media": {"/mnt/media/stacks", false, "network share"},
		"/mnt/usb":   {"/mnt/usb/stacks", false, "no Unix permissions"},
	}
	got := stacksDirDisks(mounts, "/data/agent")
	if len(got) != len(wants) {
		t.Fatalf("got %d disks %+v, want %d", len(got), got, len(wants))
	}
	for _, d := range got {
		w, ok := wants[d.Mountpoint]
		if !ok {
			t.Errorf("unexpected disk %s", d.Mountpoint)
			continue
		}
		if d.Suggested != w.suggested || d.Usable != w.usable || d.Reason != w.reason {
			t.Errorf("%s: %q usable=%v reason=%q, want %q %v %q", d.Mountpoint, d.Suggested, d.Usable, d.Reason, w.suggested, w.usable, w.reason)
		}
	}
	if got[0].FreeBytes != 21*gib {
		t.Errorf("free bytes of / = %d", got[0].FreeBytes)
	}
}

func TestHostStacksDirRoutesScope(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	for _, id := range []string{"agent-prod", "agent-dev"} {
		if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES (?, ?, 'offline')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-prod", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Agents = agents.NewService(e.db, nil, "http://dockmesh.test", "")
	e.h.Hosts = host.NewRegistry(nil, e.h.Agents)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermHostsUpdate)).Post("/hosts/{id}/stacks-dir/check", e.h.CheckHostStacksDir)
		r.With(middleware.RequirePerm(rbac.PermHostsUpdate)).Get("/hosts/{id}/stacks-dir/disks", e.h.ListHostStacksDirDisks)
	})
	e.user("boss", "admin")
	prod := e.user("prodadmin", "admin")
	e.scopeUser(prod.ID, "prod")

	tests := []struct {
		user, method, path string
		want               int
	}{
		{"boss", http.MethodPost, "/hosts/agent-dev/stacks-dir/check", http.StatusConflict},
		{"boss", http.MethodGet, "/hosts/agent-dev/stacks-dir/disks", http.StatusConflict},
		{"boss", http.MethodGet, "/hosts/missing/stacks-dir/disks", http.StatusNotFound},
		{"prodadmin", http.MethodPost, "/hosts/agent-prod/stacks-dir/check", http.StatusConflict},
		{"prodadmin", http.MethodPost, "/hosts/agent-dev/stacks-dir/check", http.StatusForbidden},
		{"prodadmin", http.MethodGet, "/hosts/agent-dev/stacks-dir/disks", http.StatusForbidden},
	}
	for _, tc := range tests {
		rr := e.do(tc.method, tc.path, e.login(tc.user).AccessToken, map[string]string{"path": "/srv/stacks"})
		if rr.Code != tc.want {
			t.Errorf("%s %s %s: %d %s, want %d", tc.user, tc.method, tc.path, rr.Code, rr.Body, tc.want)
		}
	}
}
