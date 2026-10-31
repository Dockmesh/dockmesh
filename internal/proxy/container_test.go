package proxy

import (
	"archive/tar"
	"io"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func managedContainer(s *Service, imageID string) *container.InspectResponse {
	return &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			Image: imageID,
			HostConfig: &container.HostConfig{
				NetworkMode: proxyNetworkMode,
				Binds:       append([]string(nil), proxyBinds...),
			},
		},
		Config: &container.Config{
			Cmd:    append([]string(nil), proxyCmd...),
			Labels: map[string]string{specLabel: s.specHash(), "dockmesh.component": "proxy"},
		},
	}
}

func TestMatchesSpec(t *testing.T) {
	s := NewService(nil, nil, false)
	const img = "sha256:caddy"
	if !s.matchesSpec(managedContainer(s, img), img) {
		t.Fatal("managed container rejected")
	}
	tamper := []struct {
		name string
		mod  func(*container.InspectResponse)
	}{
		{"other image", func(c *container.InspectResponse) { c.Image = "sha256:evil" }},
		{"privileged", func(c *container.InspectResponse) { c.HostConfig.Privileged = true }},
		{"cap add", func(c *container.InspectResponse) { c.HostConfig.CapAdd = []string{"SYS_ADMIN"} }},
		{"extra bind", func(c *container.InspectResponse) { c.HostConfig.Binds = append(c.HostConfig.Binds, "/:/host") }},
		{"entrypoint", func(c *container.InspectResponse) { c.Config.Entrypoint = []string{"/evil"} }},
		{"cmd", func(c *container.InspectResponse) { c.Config.Cmd = []string{"caddy", "run"} }},
		{"bridge", func(c *container.InspectResponse) { c.HostConfig.NetworkMode = "bridge" }},
		{"no label", func(c *container.InspectResponse) { delete(c.Config.Labels, specLabel) }},
	}
	for _, tt := range tamper {
		c := managedContainer(s, img)
		tt.mod(c)
		if s.matchesSpec(c, img) {
			t.Errorf("%s: tampered container kept", tt.name)
		}
	}
}

func TestTarFilesCreatesParentDirs(t *testing.T) {
	r, err := tarFiles(map[string][]byte{"caddy.json": []byte("a"), "caddy/autosave.json": []byte("b")})
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(r)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	want := []string{"caddy.json", "caddy/", "caddy/autosave.json"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
}
