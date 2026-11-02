package agents

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveEndpoints(t *testing.T) {
	tests := []struct {
		name                     string
		in                       EndpointInputs
		base, baseSrc, agent, ag string
	}{
		{
			name:  "derived from the base URL setting",
			in:    EndpointInputs{SettingBaseURL: "https://dm.lan/", DefaultBaseURL: "http://localhost:8080", AgentListen: ":8443"},
			base:  "https://dm.lan", baseSrc: SourceSetting,
			agent: "wss://dm.lan:8443/connect", ag: SourceDerived,
		},
		{
			name:  "agent URL setting is used as is",
			in:    EndpointInputs{SettingBaseURL: "https://dm.example.com", SettingAgentURL: "wss://agents.example.com:9443/connect", AgentListen: ":8443"},
			base:  "https://dm.example.com", baseSrc: SourceSetting,
			agent: "wss://agents.example.com:9443/connect", ag: SourceSetting,
		},
		{
			name: "env wins over settings",
			in: EndpointInputs{
				EnvBaseURL: "http://10.0.0.5:8080", EnvAgentURL: "wss://10.0.0.5:8443/connect",
				SettingBaseURL: "https://dm.lan", SettingAgentURL: "wss://dm.lan:8443/connect", AgentListen: ":8443",
			},
			base:  "http://10.0.0.5:8080", baseSrc: SourceEnv,
			agent: "wss://10.0.0.5:8443/connect", ag: SourceEnv,
		},
		{
			name:  "env base URL, agent URL derived from it",
			in:    EndpointInputs{EnvBaseURL: "http://192.168.1.4:8080", SettingBaseURL: "https://dm.lan", AgentListen: "0.0.0.0:9443"},
			base:  "http://192.168.1.4:8080", baseSrc: SourceEnv,
			agent: "wss://192.168.1.4:9443/connect", ag: SourceDerived,
		},
		{
			name:  "nothing set",
			in:    EndpointInputs{DefaultBaseURL: "http://localhost:8080", AgentListen: ":8443"},
			base:  "http://localhost:8080", baseSrc: SourceDefault,
			agent: "wss://localhost:8443/connect", ag: SourceDerived,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEndpoints(tt.in)
			want := Endpoints{BaseURL: tt.base, BaseSource: tt.baseSrc, AgentURL: tt.agent, AgentSource: tt.ag}
			if got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestDeriveAgentURL(t *testing.T) {
	tests := []struct {
		base, listen, want string
	}{
		{"https://dockmesh.example.com", ":8443", "wss://dockmesh.example.com:8443/connect"},
		{"http://192.168.10.5:8080", "0.0.0.0:8443", "wss://192.168.10.5:8443/connect"},
		{"http://[fd00::5]:8080", ":8443", "wss://[fd00::5]:8443/connect"},
		{"https://dm.lan", "", "wss://dm.lan:8443/connect"},
		{"https://dm.lan", "garbage", "wss://dm.lan:8443/connect"},
		{"", ":8443", "wss://localhost:8443/connect"},
	}
	for _, tt := range tests {
		if got := DeriveAgentURL(tt.base, tt.listen); got != tt.want {
			t.Errorf("DeriveAgentURL(%q, %q) = %q, want %q", tt.base, tt.listen, got, tt.want)
		}
	}
}

func TestValidateURLs(t *testing.T) {
	tests := []struct {
		name  string
		agent bool
		in    string
		ok    bool
	}{
		{"https base", false, "https://dm.example.com", true},
		{"http base with port", false, "http://192.168.1.4:8080", true},
		{"base without scheme", false, "dm.example.com", false},
		{"base with quote", false, "https://dm.example.com'$(id)", false},
		{"base with query", false, "https://dm.example.com/?x=1", false},
		{"base with credentials", false, "https://u:p@dm.example.com", false},
		{"wss agent", true, "wss://dm.example.com:8443/connect", true},
		{"agent IP", true, "wss://10.0.0.5:8443/connect", true},
		{"agent https scheme", true, "https://dm.example.com:8443/connect", false},
		{"agent ws scheme", true, "ws://dm.example.com:8443/connect", false},
		{"agent bad host", true, "wss://dm_example.com:8443/connect", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.agent {
				err = ValidateAgentURL(tt.in)
			} else {
				err = ValidateBaseURL(tt.in)
			}
			if (err == nil) != tt.ok {
				t.Errorf("validate %q: err = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestReloadEndpointsAddsAgentHostToCert(t *testing.T) {
	svc, mgr := newTestService(t)
	caBefore := mgr.CAFingerprint()
	ep := Endpoints{BaseURL: "https://dm.lan", AgentURL: "wss://dm.lan:8443/connect", AgentSource: SourceDerived}
	svc.SetEndpointResolver(func() Endpoints { return ep })

	if mgr.ServerCertCovers("dm.lan") {
		t.Fatal("test setup: cert already covers dm.lan")
	}
	got, err := svc.ReloadEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if got != ep || svc.Endpoints() != ep {
		t.Errorf("endpoints = %+v, want %+v", svc.Endpoints(), ep)
	}
	if !mgr.ServerCertCovers("dm.lan") {
		t.Error("cert does not cover the agent URL host after reload")
	}
	if !mgr.ServerCertCovers("127.0.0.1") {
		t.Error("re-issued cert dropped the IP SAN it had before")
	}
	if mgr.CAFingerprint() != caBefore {
		t.Error("CA changed; enrolled agents would stop trusting the server")
	}
	tc, err := mgr.ServerTLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReloadEndpoints(); err != nil {
		t.Fatal(err)
	}
	if tc2, _ := mgr.ServerTLSCertificate(); tc2 != tc {
		t.Error("second reload with the same URL re-issued the cert again")
	}

	res, err := svc.Create(context.Background(), "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.InstallHint, "curl -fsSL 'https://dm.lan/install/agent.sh?") {
		t.Errorf("install hint uses old URL: %s", res.InstallHint)
	}
	if res.AgentURL != ep.AgentURL || res.EnrollURL != "https://dm.lan:8443/enroll" {
		t.Errorf("agent/enroll URL = %q / %q", res.AgentURL, res.EnrollURL)
	}
}

func TestCheckReachability(t *testing.T) {
	svc, mgr := newTestService(t)
	tlsCfg, err := ServerTLSConfig(mgr)
	if err != nil {
		t.Fatal(err)
	}
	own := httptest.NewUnstartedServer(http.NotFoundHandler())
	own.TLS = tlsCfg
	own.StartTLS()
	t.Cleanup(own.Close)
	foreign := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(foreign.Close)
	ownAddr := own.Listener.Addr().String()
	foreignAddr := foreign.Listener.Addr().String()

	type want struct {
		ok     bool
		failID string
		warnID string
		text   string
	}
	tests := []struct {
		name     string
		agentURL string
		listener bool
		lookup   func(context.Context, string) ([]string, error)
		target   string
		want     want
	}{
		{
			name: "own listener", agentURL: "wss://dm.lan:8443/connect", listener: true, target: ownAddr,
			want: want{ok: true},
		},
		{
			name: "proxy terminates TLS", agentURL: "wss://dm.lan:8443/connect", listener: true, target: foreignAddr,
			want: want{failID: "tls", text: "Cloudflare"},
		},
		{
			name: "port closed", agentURL: "wss://dm.lan:8443/connect", listener: true, target: "",
			want: want{ok: true, warnID: "connect", text: "hairpin"},
		},
		{
			name: "name does not resolve", agentURL: "wss://dm.lan:8443/connect", listener: true,
			lookup: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
			want:   want{failID: "dns"},
		},
		{
			name: "localhost", agentURL: "wss://localhost:8443/connect", listener: true,
			want: want{failID: "url"},
		},
		{
			name: "listener off", agentURL: "wss://dm.lan:8443/connect", listener: false,
			want: want{failID: "listener"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := Endpoints{BaseURL: "https://dm.lan", AgentURL: tt.agentURL}
			svc.SetEndpointResolver(func() Endpoints { return ep })
			if _, err := svc.ReloadEndpoints(); err != nil {
				t.Fatal(err)
			}
			svc.SetListenerEnabled(tt.listener)
			svc.lookup = tt.lookup
			if svc.lookup == nil {
				svc.lookup = func(context.Context, string) ([]string, error) { return []string{"192.0.2.10"}, nil }
			}
			svc.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
				if tt.target == "" {
					return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
				}
				var d net.Dialer
				return d.DialContext(ctx, network, tt.target)
			}
			r := svc.CheckReachability(context.Background())
			if r.OK != tt.want.ok {
				t.Errorf("ok = %v, want %v; checks %+v", r.OK, tt.want.ok, r.Checks)
			}
			var all strings.Builder
			for _, c := range r.Checks {
				all.WriteString(c.Message)
				if tt.want.failID != "" && c.ID == tt.want.failID && c.Status != CheckFail {
					t.Errorf("check %s = %s, want fail", c.ID, c.Status)
				}
				if tt.want.warnID != "" && c.ID == tt.want.warnID && c.Status != CheckWarn {
					t.Errorf("check %s = %s, want warn", c.ID, c.Status)
				}
			}
			if tt.want.text != "" && !strings.Contains(all.String(), tt.want.text) {
				t.Errorf("messages do not mention %q: %s", tt.want.text, all.String())
			}
		})
	}
}
