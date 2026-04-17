package registries

import "testing"

func TestRegistryForImage(t *testing.T) {
	cases := map[string]string{
		"nginx":                            "docker.io",
		"nginx:alpine":                     "docker.io",
		"library/nginx":                    "docker.io",
		"foo/bar":                          "docker.io",
		"foo/bar:v1":                       "docker.io",
		"ghcr.io/foo/bar":                  "ghcr.io",
		"ghcr.io/foo/bar:latest":           "ghcr.io",
		"registry.gitlab.com/grp/proj:v1":  "registry.gitlab.com",
		"registry:5000/foo/bar":            "registry:5000",
		"localhost/foo":                    "localhost",
		"localhost:5000/foo":               "localhost:5000",
		"public.ecr.aws/ubuntu/ubuntu:22":  "public.ecr.aws",
	}
	for ref, want := range cases {
		got := RegistryForImage(ref)
		if got != want {
			t.Errorf("RegistryForImage(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"ghcr.io":            "ghcr.io",
		"GHCR.IO":            "ghcr.io",
		"https://ghcr.io":    "ghcr.io",
		"https://ghcr.io/":   "ghcr.io",
		"http://ghcr.io//":   "ghcr.io",
		"  ghcr.io  ":        "ghcr.io",
		"registry:5000":      "registry:5000",
		"https://harbor.example.com/v2/": "harbor.example.com/v2",
	}
	for raw, want := range cases {
		got := NormalizeURL(raw)
		if got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
