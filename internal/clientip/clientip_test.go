package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		trusted    string
		remote     string
		headers    map[string]string
		wantIP     string
		wantSecure bool
		wantNotice bool
	}{
		{name: "direct client", remote: "203.0.113.7:5555", wantIP: "203.0.113.7"},
		{name: "spoofed xff from untrusted peer", remote: "203.0.113.7:5555",
			headers: map[string]string{"X-Forwarded-For": "1.2.3.4"}, wantIP: "203.0.113.7", wantNotice: true},
		{name: "spoofed x-real-ip from untrusted peer", remote: "203.0.113.7:5555",
			headers: map[string]string{"X-Real-IP": "1.2.3.4"}, wantIP: "203.0.113.7", wantNotice: true},
		{name: "spoofed proto from untrusted peer", remote: "203.0.113.7:5555",
			headers: map[string]string{"X-Forwarded-Proto": "https"}, wantIP: "203.0.113.7"},
		{name: "loopback proxy trusted by default", remote: "127.0.0.1:4000",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.9", "X-Forwarded-Proto": "https"},
			wantIP:  "198.51.100.9", wantSecure: true},
		{name: "ipv6 loopback proxy", remote: "[::1]:4000",
			headers: map[string]string{"X-Real-IP": "198.51.100.9"}, wantIP: "198.51.100.9"},
		{name: "configured proxy replaces default", trusted: "10.0.0.0/8", remote: "127.0.0.1:4000",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.9"}, wantIP: "127.0.0.1", wantNotice: true},
		{name: "client-prepended hop ignored", trusted: "10.0.0.2", remote: "10.0.0.2:80",
			headers: map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9"}, wantIP: "198.51.100.9"},
		{name: "proxy chain", trusted: "10.0.0.0/8", remote: "10.0.0.2:80",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.9, 10.0.0.3"}, wantIP: "198.51.100.9"},
		{name: "garbage hop stops the walk", trusted: "10.0.0.2", remote: "10.0.0.2:80",
			headers: map[string]string{"X-Forwarded-For": "nonsense"}, wantIP: "10.0.0.2"},
		{name: "trusted peer without headers", trusted: "10.0.0.2", remote: "10.0.0.2:80", wantIP: "10.0.0.2"},
		{name: "invalid setting falls back to default", trusted: "not-a-cidr", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.9"}, wantIP: "198.51.100.9"},
		{name: "nginx real-ip with client xff fails closed", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Real-IP": "198.51.100.9", "X-Forwarded-For": "6.6.6.6"}, wantIP: "127.0.0.1"},
		{name: "caddy xff with client real-ip fails closed", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Real-IP": "6.6.6.6", "X-Forwarded-For": "198.51.100.9"}, wantIP: "127.0.0.1"},
		{name: "both headers agree", remote: "127.0.0.1:1",
			headers: map[string]string{"X-Real-IP": "198.51.100.9", "X-Forwarded-For": "6.6.6.6, 198.51.100.9"}, wantIP: "198.51.100.9"},
		{name: "ipv4-mapped peer", trusted: "10.0.0.2", remote: "[::ffff:10.0.0.2]:80",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.9"}, wantIP: "198.51.100.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trusted := tt.trusted
			res := New(func() string { return trusted })
			noticed := false
			res.OnUntrustedForward = func(string) { noticed = true }

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			var gotIP string
			var gotSecure bool
			res.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotIP, gotSecure = IP(r), Secure(r)
			})).ServeHTTP(httptest.NewRecorder(), req)

			if gotIP != tt.wantIP {
				t.Errorf("IP = %q, want %q", gotIP, tt.wantIP)
			}
			if gotSecure != tt.wantSecure {
				t.Errorf("Secure = %v, want %v", gotSecure, tt.wantSecure)
			}
			if noticed != tt.wantNotice {
				t.Errorf("untrusted-forward callback = %v, want %v", noticed, tt.wantNotice)
			}
		})
	}
}

func TestIPWithoutMiddlewareIgnoresHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := IP(req); got != "203.0.113.7" {
		t.Fatalf("IP = %q, want peer address", got)
	}
}

func TestParseList(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "10.0.0.0/8, 192.168.1.5", want: 2},
		{in: "fd00::/8\n::1", want: 2},
		{in: "10.0.0.0/33", wantErr: true},
		{in: "example.com", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseList(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseList(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if len(got) != tt.want {
			t.Errorf("ParseList(%q) = %d entries, want %d", tt.in, len(got), tt.want)
		}
	}
}

func TestLimitKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"198.51.100.7", "198.51.100.7"},
		{"::ffff:198.51.100.7", "198.51.100.7"},
		{"2001:db8:1:2:aaaa::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:bbbb::9", "2001:db8:1:2::/64"},
		{"not-an-ip", "not-an-ip"},
	}
	for _, tt := range tests {
		if got := LimitKey(tt.in); got != tt.want {
			t.Errorf("LimitKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
