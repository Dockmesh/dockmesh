package proxy

import (
	"archive/tar"
	"io"
	"testing"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

func managedContainer(s *Service, imageID string) *dtypes.ContainerJSON {
	return &dtypes.ContainerJSON{
		ContainerJSONBase: &dtypes.ContainerJSONBase{
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
	if !matchesSpec(managedContainer(s, img), s.currentImage(), img) {
		t.Fatal("managed container rejected")
	}
	tamper := []struct {
		name string
		mod  func(*dtypes.ContainerJSON)
	}{
		{"other image", func(c *dtypes.ContainerJSON) { c.Image = "sha256:evil" }},
		{"privileged", func(c *dtypes.ContainerJSON) { c.HostConfig.Privileged = true }},
		{"cap add", func(c *dtypes.ContainerJSON) { c.HostConfig.CapAdd = []string{"SYS_ADMIN"} }},
		{"extra bind", func(c *dtypes.ContainerJSON) { c.HostConfig.Binds = append(c.HostConfig.Binds, "/:/host") }},
		{"entrypoint", func(c *dtypes.ContainerJSON) { c.Config.Entrypoint = []string{"/evil"} }},
		{"cmd", func(c *dtypes.ContainerJSON) { c.Config.Cmd = []string{"caddy", "run"} }},
		{"bridge", func(c *dtypes.ContainerJSON) { c.HostConfig.NetworkMode = "bridge" }},
		{"no label", func(c *dtypes.ContainerJSON) { delete(c.Config.Labels, specLabel) }},
	}
	for _, tt := range tamper {
		c := managedContainer(s, img)
		tt.mod(c)
		if matchesSpec(c, s.currentImage(), img) {
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
