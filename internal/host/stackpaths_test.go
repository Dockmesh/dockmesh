package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
)

func TestResolvesStackPaths(t *testing.T) {
	withResolver := NewLocal(nil)
	withResolver.SetStackDirResolver(func(string) (string, error) { return "/stacks/x", nil })
	tests := []struct {
		name string
		h    Host
		want bool
	}{
		{"server", withResolver, true},
		{"server without stack directories", NewLocal(nil), false},
		{"agent that takes stack files", deployAgent(t, []string{agents.CapStackFiles}, nil), true},
		{"older agent", deployAgent(t, nil, nil), false},
		{"offline agent", NewRemote("a1", "edge-1", nil), false},
		{"no host", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolvesStackPaths(tt.h); got != tt.want {
				t.Fatalf("ResolvesStackPaths = %v, want %v", got, tt.want)
			}
		})
	}
}

// A compose file merged with an override on the server deploys with its
// relative paths in the deploying host's stack directory.
func TestLocalDeployResolvesMergedPaths(t *testing.T) {
	serverDir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"compose.yaml":      "services:\n  web:\n    image: nginx\n    volumes:\n      - ./html:/html\n      - ../shared:/shared\n      - /srv/abs:/abs\n",
		"compose.prod.yaml": "services:\n  web:\n    image: nginx:1.26\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(serverDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, merged, err := compose.MergeEnvironment(context.Background(), serverDir, "app", "", "prod", true)
	if err != nil {
		t.Fatal(err)
	}
	hostDir := filepath.Join(t.TempDir(), "stacks", "app")
	h := NewLocal(nil)
	h.SetStackDirResolver(func(string) (string, error) { return hostDir, nil })
	dir, cleanup, err := writeStagingDir("app", merged, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	proj, warnings, err := h.loadStaged(context.Background(), dir, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/html":   filepath.Join(hostDir, "html"),
		"/shared": filepath.Join(filepath.Dir(hostDir), "shared"),
		"/abs":    "/srv/abs",
	}
	for _, v := range proj.Services["web"].Volumes {
		if want[v.Target] != v.Source {
			t.Errorf("%s: source %s, want %s", v.Target, v.Source, want[v.Target])
		}
	}
	if len(warnings) != 1 || warnings[0] != "service web: bind ../shared points outside the stack directory, to "+want["/shared"] {
		t.Errorf("warnings = %q", warnings)
	}
}
