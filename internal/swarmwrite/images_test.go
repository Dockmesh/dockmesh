package swarmwrite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/dockmesh/dockmesh/internal/agents"
)

type fakeRegistry struct {
	*fakeDaemon
	auths map[string]string
}

func (f *fakeRegistry) DistributionInspect(_ context.Context, ref string, o client.DistributionInspectOptions) (client.DistributionInspectResult, error) {
	f.auths[ref] = o.EncodedRegistryAuth
	if ref == "gone:1" {
		return client.DistributionInspectResult{}, errors.New("manifest unknown")
	}
	return client.DistributionInspectResult{DistributionInspect: registry.DistributionInspect{Descriptor: ocispec.Descriptor{Digest: "sha256:abc"}}}, nil
}

func TestCheckImages(t *testing.T) {
	f := &fakeRegistry{fakeDaemon: &fakeDaemon{}, auths: map[string]string{}}
	out, err := CheckImages(context.Background(), f, agents.SwarmImageCheckReq{Images: []agents.SwarmImageRef{
		{Image: "nginx:1.29", RegistryAuth: "blob"}, {Image: "gone:1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].Digest != "sha256:abc" || out.Results[1].Error != "the registry has no such image or tag" || f.auths["nginx:1.29"] != "blob" {
		t.Fatalf("%+v %v", out, f.auths)
	}
	for _, bad := range []agents.SwarmImageCheckReq{
		{},
		{Images: []agents.SwarmImageRef{{Image: "nginx@sha256:" + strings.Repeat("a", 64)}}},
		{Images: []agents.SwarmImageRef{{Image: "a"}, {Image: "a"}}},
		{Images: []agents.SwarmImageRef{{Image: "../containers/abc"}}},
	} {
		if _, err := CheckImages(context.Background(), f, bad); !errors.Is(err, agents.ErrSwarmWriteRefused) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := CheckImages(context.Background(), &fakeRegistry{fakeDaemon: &fakeDaemon{worker: true}, auths: map[string]string{}}, agents.SwarmImageCheckReq{Images: []agents.SwarmImageRef{{Image: "a"}}}); !errors.Is(err, agents.ErrNotSwarmManager) {
		t.Fatalf("worker: %v", err)
	}
}
