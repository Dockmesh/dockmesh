package compose

import (
	"context"
	"testing"
)

type staticAuth string

func (a staticAuth) ResolveAuthForImage(context.Context, string) (string, error) { return string(a), nil }

func TestResolveImageAuthPrefersRequestAuth(t *testing.T) {
	tests := []struct {
		name     string
		resolver authResolver
		ctxAuth  map[string]string
		image    string
		want     string
	}{
		{"request auth wins", staticAuth("server"), map[string]string{"app:1": "req"}, "app:1", "req"},
		{"other image falls back", staticAuth("server"), map[string]string{"app:1": "req"}, "db:1", "server"},
		{"agent without resolver", nil, map[string]string{"app:1": "req"}, "app:1", "req"},
		{"nothing", nil, nil, "app:1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			if tt.resolver != nil {
				s.SetAuthResolver(tt.resolver)
			}
			ctx := WithRegistryAuth(context.Background(), tt.ctxAuth)
			if got := s.resolveImageAuth(ctx, tt.image); got != tt.want {
				t.Errorf("auth = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImagesOf(t *testing.T) {
	yaml := "include:\n  - /etc/passwd\nservices:\n  app:\n    image: registry.example.com/app:${TAG}\n  db:\n    image: postgres:${PG:-16}\n  built:\n    build: .\n"
	got := ImagesOf(yaml, "# c\nTAG=2\n")
	want := map[string]bool{"registry.example.com/app:2": true, "postgres:16": true}
	if len(got) != len(want) {
		t.Fatalf("images = %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected image %q", g)
		}
	}
	if got := ImagesOf("not: [yaml", ""); got != nil {
		t.Errorf("invalid yaml: %v", got)
	}
}
