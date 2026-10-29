package updater

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func TestCarryVolumes(t *testing.T) {
	old := []dtypes.MountPoint{
		{Type: mount.TypeVolume, Name: "3f9a", Destination: "/data", RW: true},
		{Type: mount.TypeVolume, Name: "named", Destination: "/named", RW: true},
		{Type: mount.TypeVolume, Name: "c0de", Destination: "/compose-anon", RW: true},
		{Type: mount.TypeVolume, Name: "ro", Destination: "/ro", RW: false},
		{Type: mount.TypeBind, Source: "/srv/x", Destination: "/bind", RW: true},
		{Type: mount.TypeVolume, Name: "replaced", Destination: "/explicit", RW: true},
	}
	cfg := &container.Config{Volumes: map[string]struct{}{"/data": {}}}
	hostCfg := &container.HostConfig{
		Binds: []string{"named:/named", "/srv/x:/bind:ro"},
		Mounts: []mount.Mount{
			{Type: mount.TypeVolume, Target: "/compose-anon"},
			{Type: mount.TypeVolume, Source: "fresh", Target: "/explicit"},
		},
	}
	carryVolumes(old, cfg, hostCfg)

	want := []mount.Mount{
		{Type: mount.TypeVolume, Source: "c0de", Target: "/compose-anon"},
		{Type: mount.TypeVolume, Source: "fresh", Target: "/explicit"},
		{Type: mount.TypeVolume, Source: "3f9a", Target: "/data"},
		{Type: mount.TypeVolume, Source: "ro", Target: "/ro", ReadOnly: true},
	}
	if !reflect.DeepEqual(hostCfg.Mounts, want) {
		t.Fatalf("mounts:\n got %+v\nwant %+v", hostCfg.Mounts, want)
	}
	if _, ok := cfg.Volumes["/data"]; ok {
		t.Fatal("/data still declared as a fresh anonymous volume")
	}
}

func TestBindTarget(t *testing.T) {
	tests := []struct{ in, want string }{
		{"vol:/data", "/data"},
		{"/srv:/srv:ro", "/srv"},
		{"a:/x:rw", "/x"},
		{`C:\data:/data`, "/data"},
		{"bogus", ""},
	}
	for _, tt := range tests {
		if got := bindTarget(tt.in); got != tt.want {
			t.Errorf("bindTarget(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBaseRef(t *testing.T) {
	tests := []struct{ in, want string }{
		{"nginx:1.27", "nginx:1.27"},
		{"nginx:1.27-rollback-1700000000", "nginx:1.27"},
		{"nginx:rollback-1700000000", "nginx:latest"},
		{"localhost:5000/app:v2-rollback-17", "localhost:5000/app:v2"},
		{"localhost:5000/app", "localhost:5000/app"},
	}
	for _, tt := range tests {
		if got := baseRef(tt.in); got != tt.want {
			t.Errorf("baseRef(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := baseRef(sanitizeTag("ghcr.io/o/app:1", 42)); got != "ghcr.io/o/app:1" {
		t.Errorf("baseRef(sanitizeTag) = %q", got)
	}
}

func TestRepoDigestFor(t *testing.T) {
	const a = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const b = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	digests := []string{"mirror.local/library/nginx@" + a, "nginx@" + b}
	tests := []struct {
		ref, want string
	}{
		{"nginx:1.27", b},
		{"docker.io/library/nginx:latest", b},
		{"mirror.local/library/nginx:1.27", a},
		{"redis:7", ""},
	}
	for _, tt := range tests {
		if got := repoDigestFor(tt.ref, digests); got != tt.want {
			t.Errorf("repoDigestFor(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRollbackTagsToPrune(t *testing.T) {
	tags := []string{
		"nginx:1.27-rollback-100",
		"nginx:1.27-rollback-300",
		"nginx:1.27-rollback-200",
		"nginx:1.27-rollback-400",
		"nginx:1.27",
		"nginx:1.27-rollback-x",
		"nginx:1.28-rollback-50",
	}
	got := rollbackTagsToPrune("nginx:1.27", tags, 2)
	want := []string{"nginx:1.27-rollback-100", "nginx:1.27-rollback-200"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := rollbackTagsToPrune("nginx:1.27", tags, 4); got != nil {
		t.Fatalf("keep 4: got %v", got)
	}
}

func TestShouldNotify(t *testing.T) {
	tests := []struct {
		name     string
		info     UpdateInfo
		notified string
		want     bool
	}{
		{"new update", UpdateInfo{UpdateAvailable: true, RemoteDigest: "d1"}, "", true},
		{"already notified", UpdateInfo{UpdateAvailable: true, RemoteDigest: "d1"}, "d1", false},
		{"newer release", UpdateInfo{UpdateAvailable: true, RemoteDigest: "d2"}, "d1", true},
		{"up to date", UpdateInfo{RemoteDigest: "d2"}, "d1", false},
	}
	for _, tt := range tests {
		if got := shouldNotify(tt.info, tt.notified); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestWatcherPersistsChecks(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	w := NewWatcher(nil, database, 0)
	info := UpdateInfo{Image: "nginx:1.27", UpdateAvailable: true, LocalDigest: "l", RemoteDigest: "r", CheckedAt: time.Now()}
	w.store(ctx, info, "r")
	stale := UpdateInfo{Image: "old:1", CheckedAt: time.Now().Add(-2 * checkRetention)}
	w.store(ctx, stale, "")

	restarted := NewWatcher(nil, database, 0)
	restarted.load(ctx)
	got, ok := restarted.Get("nginx:1.27")
	if !ok || !got.UpdateAvailable || got.RemoteDigest != "r" {
		t.Fatalf("restored info = %+v, %v", got, ok)
	}
	if shouldNotify(got, restarted.notified["nginx:1.27"]) {
		t.Fatal("restart would notify again")
	}

	restarted.MarkUpdated(ctx, "nginx:1.27")
	if got, _ := restarted.Get("nginx:1.27"); got.UpdateAvailable {
		t.Fatal("MarkUpdated left the flag set")
	}

	restarted.expire(ctx)
	if _, ok := restarted.Get("old:1"); ok {
		t.Fatal("stale entry not expired from cache")
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM image_update_checks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("stored rows after expire = %d, want 1", n)
	}
}
