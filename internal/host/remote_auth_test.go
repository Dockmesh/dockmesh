package host

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

type fakeRegistryAuth struct {
	host   string
	images []string
}

func (f *fakeRegistryAuth) AuthForImages(_ context.Context, hostID string, images []string) (map[string]string, error) {
	f.host, f.images = hostID, images
	return map[string]string{"registry.example.com/app:2": "blob"}, nil
}

func TestRemoteRegistryAuthCoversStackImages(t *testing.T) {
	fa := &fakeRegistryAuth{}
	h := &RemoteHost{id: "agent1", auth: fa}
	yaml := "services:\n  app:\n    image: registry.example.com/app:${TAG}\n  db:\n    image: postgres:16\n"
	got := h.registryAuth(context.Background(), "web", yaml, "TAG=2\n")
	if !reflect.DeepEqual(got, map[string]string{"registry.example.com/app:2": "blob"}) {
		t.Errorf("auth = %v", got)
	}
	sort.Strings(fa.images)
	if fa.host != "agent1" || !reflect.DeepEqual(fa.images, []string{"postgres:16", "registry.example.com/app:2"}) {
		t.Errorf("asked for %s %v", fa.host, fa.images)
	}
	if got := (&RemoteHost{id: "agent1"}).registryAuth(context.Background(), "web", yaml, ""); got != nil {
		t.Errorf("auth without a resolver = %v", got)
	}
}
