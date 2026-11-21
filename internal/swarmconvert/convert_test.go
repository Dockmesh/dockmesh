package swarmconvert

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/dockmesh/dockmesh/internal/compose"
)

func convertYAML(t *testing.T, src string) (*Result, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadProject(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	return Convert(project, "unit")
}

func loadProject(dir, stack string) (*types.Project, error) {
	return compose.LoadProject(context.Background(), dir, stack, "")
}

func TestConvertRefuses(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"port range to one port", "services:\n  a:\n    image: alpine\n    ports:\n      - \"8080-8081:80\"\n", "not a single port"},
		{"published port out of range", "services:\n  a:\n    image: alpine\n    ports:\n      - target: 80\n        published: \"70000\"\n", "not a single port"},
		{"unknown restart condition", "services:\n  a:\n    image: alpine\n    deploy:\n      restart_policy:\n        condition: bogus\n", "restart_policy.condition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := convertYAML(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestConvertEnvironmentWithoutValue(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    environment:\n      - KEY_ONLY\n      - SET=1\n")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Services[0].TaskTemplate.ContainerSpec.Env
	want := []string{"KEY_ONLY", "SET=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env %v, want %v", got, want)
	}
}

func TestConvertNamespaceLabelWins(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    labels:\n      com.docker.stack.namespace: other\n    deploy:\n      labels:\n        com.docker.stack.namespace: other\n        com.docker.stack.image: other\n")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Services[0]
	if s.Labels[LabelNamespace] != "unit" || s.Labels[LabelImage] != "alpine" || s.TaskTemplate.ContainerSpec.Labels[LabelNamespace] != "unit" {
		t.Fatalf("labels %v / %v", s.Labels, s.TaskTemplate.ContainerSpec.Labels)
	}
}

func TestConvertNeedsStackAndImage(t *testing.T) {
	if _, err := Convert(nil, ""); err == nil {
		t.Fatal("empty stack name accepted")
	}
	if _, err := convertYAML(t, "services:\n  a:\n    build: .\n"); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("err %v, want no image", err)
	}
}
