package registries

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/moby/moby/api/types/registry"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

func userOf(t *testing.T, blob string) string {
	t.Helper()
	if blob == "" {
		return ""
	}
	b, err := base64.URLEncoding.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}
	var a registry.AuthConfig
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	return a.Username
}

func TestResolveUsesHostTags(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.New(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	s := New(database, sec)
	s.SetHostTags(func(host string) []string {
		return map[string][]string{"agent1": {"prod"}, "agent2": {"dev"}}[host]
	})
	for _, in := range []Input{
		{Name: "prod", URL: "registry.example.com", Username: "prod-user", Password: "p", ScopeTags: []string{"prod"}},
		{Name: "ghcr", URL: "ghcr.io", Username: "ghcr-user", Password: "p"},
	} {
		if _, err := s.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		host, image, want string
	}{
		{"agent1", "registry.example.com/app:1", "prod-user"},
		{"agent2", "registry.example.com/app:1", ""},
		{"local", "registry.example.com/app:1", ""},
		{"agent2", "ghcr.io/acme/web", "ghcr-user"},
		{"local", "nginx", ""},
	}
	for _, tt := range tests {
		blob, err := s.ForHost(tt.host).ResolveAuthForImage(ctx, tt.image)
		if err != nil {
			t.Fatal(err)
		}
		if got := userOf(t, blob); got != tt.want {
			t.Errorf("%s on %s: user %q, want %q", tt.image, tt.host, got, tt.want)
		}
	}

	auth, err := s.AuthForImages(ctx, "agent1", []string{"registry.example.com/app:1", "nginx", "ghcr.io/acme/web", "", "nginx"})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(auth))
	for k := range auth {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"ghcr.io/acme/web", "registry.example.com/app:1"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("auth for images = %v, want %v", keys, want)
	}
}
