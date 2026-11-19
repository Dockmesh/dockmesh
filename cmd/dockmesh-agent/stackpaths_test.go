package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
)

// A compose file the server merged with an override keeps its relative
// paths, and the agent resolves them against its own stack directory.
func TestAgentResolvesMergedPaths(t *testing.T) {
	serverDir := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"compose.yaml":      "services:\n  web:\n    image: nginx\n    volumes:\n      - ./html:/html\n      - ../shared:/shared\n      - /srv/abs:/abs\nconfigs:\n  cfg:\n    file: ./cfg.txt\n",
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
	stacksDir := filepath.Join(t.TempDir(), "agent-stacks")
	t.Setenv("DOCKMESH_STACKS_DIR", stacksDir)
	t.Setenv("DOCKMESH_EARLIER_STACKS_DIRS", "")
	staging, cleanup, err := compose.NewStagingDir(t.TempDir(), "app-")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(staging, "compose.yaml"), []byte(merged), 0o600); err != nil {
		t.Fatal(err)
	}
	proj, warnings, err := agentLoadStaged(context.Background(), staging, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	stackDir := filepath.Join(stacksDir, "app")
	want := map[string]string{
		"/html":   filepath.Join(stackDir, "html"),
		"/shared": filepath.Join(stacksDir, "shared"),
		"/abs":    "/srv/abs",
		"config":  filepath.Join(stackDir, "cfg.txt"),
	}
	got := map[string]string{"config": proj.Configs["cfg"].File}
	for _, v := range proj.Services["web"].Volumes {
		got[v.Target] = v.Source
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %s, want %s", k, got[k], w)
		}
	}
	if len(warnings) != 1 || warnings[0] != "service web: bind ../shared points outside the stack directory, to "+want["/shared"] {
		t.Errorf("warnings = %q", warnings)
	}
}
