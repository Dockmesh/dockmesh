//go:build unix

package main

import "testing"

func TestHealthURL(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"", "http://127.0.0.1:8080/api/v1/health"},
		{":8080", "http://127.0.0.1:8080/api/v1/health"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/api/v1/health"},
		{"[::]:9000", "http://[::1]:9000/api/v1/health"},
		{"192.168.1.5:8080", "http://192.168.1.5:8080/api/v1/health"},
		{"[fd00::5]:8080", "http://[fd00::5]:8080/api/v1/health"},
		{"evil.example.com:8080", "http://127.0.0.1:8080/api/v1/health"},
		{"garbage", "http://127.0.0.1:8080/api/v1/health"},
	}
	for _, tt := range tests {
		if got := healthURL(tt.addr); got != tt.want {
			t.Errorf("healthURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestServerFiles(t *testing.T) {
	files := serverFiles("/usr/local/bin/dockmesh")
	if !files[0].KeepPrev || files[0].Optional || files[0].Dest != "/usr/local/bin/dockmesh" {
		t.Fatalf("first file %+v", files[0])
	}
	want := map[string]string{
		"dmctl":                      "/usr/local/bin/dmctl",
		"dockmesh-agent-linux-amd64": "/usr/local/share/dockmesh/bin/dockmesh-agent-linux-amd64",
		"install-agent.sh":           "/usr/local/share/dockmesh/install-agent.sh",
	}
	for _, f := range files[1:] {
		if !f.Optional || f.KeepPrev {
			t.Errorf("%s: %+v", f.Name, f)
		}
		if w, ok := want[f.Name]; ok && f.Dest != w {
			t.Errorf("%s goes to %s, want %s", f.Name, f.Dest, w)
		}
	}
}
