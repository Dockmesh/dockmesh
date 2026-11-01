package updater

import "testing"

func TestParseDockerHubRef(t *testing.T) {
	cases := []struct {
		in        string
		ns, repo  string
		tag       string
		ok        bool
	}{
		{"nginx", "library", "nginx", "latest", true},
		{"nginx:alpine", "library", "nginx", "alpine", true},
		{"bitnami/postgres:16", "bitnami", "postgres", "16", true},
		{"bitnami/postgres", "bitnami", "postgres", "latest", true},
		{"ghcr.io/owner/app:v1", "", "", "", false},
		{"quay.io/coreos/etcd:3.5", "", "", "", false},
		{"localhost:5000/app:dev", "", "", "", false},
	}
	for _, tc := range cases {
		ns, repo, tag, ok := parseDockerHubRef(tc.in)
		if ok != tc.ok || ns != tc.ns || repo != tc.repo || tag != tc.tag {
			t.Errorf("parseDockerHubRef(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				tc.in, ns, repo, tag, ok, tc.ns, tc.repo, tc.tag, tc.ok)
		}
	}
}

func TestExtractGitHubRepo(t *testing.T) {
	cases := []struct {
		desc  string
		owner string
		repo  string
		ok    bool
	}{
		{"Source: https://github.com/nginx/nginx", "nginx", "nginx", true},
		{"See github.com/prometheus/prometheus for details.", "prometheus", "prometheus", true},
		{"visit: https://github.com/grafana/grafana/releases", "grafana", "grafana", true},
		{"my-github-token is secret", "", "", false},
		{"no github here", "", "", false},
	}
	for _, tc := range cases {
		o, r, ok := extractGitHubRepo(tc.desc)
		if ok != tc.ok || o != tc.owner || r != tc.repo {
			t.Errorf("extractGitHubRepo(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.desc, o, r, ok, tc.owner, tc.repo, tc.ok)
		}
	}
}
