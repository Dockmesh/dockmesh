package agents

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestResolveEndpointsSkipsInvalidValues(t *testing.T) {
	tests := []struct {
		name              string
		in                EndpointInputs
		base, baseSrc, ag string
		agent             string
	}{
		{
			name: "stored base URL with shell characters",
			in:   EndpointInputs{SettingBaseURL: "https://x/$(id>/tmp/p)", DefaultBaseURL: "http://localhost:8080", AgentListen: ":8443"},
			base: "http://localhost:8080", baseSrc: SourceDefault,
			agent: "wss://localhost:8443/connect", ag: SourceDerived,
		},
		{
			name: "bad env base URL falls back to the setting",
			in:   EndpointInputs{EnvBaseURL: "dm.lan", SettingBaseURL: "https://dm.lan", AgentListen: ":8443"},
			base: "https://dm.lan", baseSrc: SourceSetting,
			agent: "wss://dm.lan:8443/connect", ag: SourceDerived,
		},
		{
			name: "stored agent URL with wrong scheme is derived instead",
			in:   EndpointInputs{SettingBaseURL: "https://dm.lan", SettingAgentURL: "https://dm.lan:8443/connect", AgentListen: ":8443"},
			base: "https://dm.lan", baseSrc: SourceSetting,
			agent: "wss://dm.lan:8443/connect", ag: SourceDerived,
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

func TestValidateURLsRefuseCertUnsafeHosts(t *testing.T) {
	tests := []struct {
		in    string
		agent bool
	}{
		{"https://*.example.com", false},
		{"wss://*.example.com:8443/connect", true},
		{"https://dm_example.com", false},
		{"https://bücher.example", false},
	}
	for _, tt := range tests {
		var err error
		if tt.agent {
			err = ValidateAgentURL(tt.in)
		} else {
			err = ValidateBaseURL(tt.in)
		}
		if err == nil {
			t.Errorf("%q accepted", tt.in)
		}
	}
}

func TestRefuseLinkLocal(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"192.168.10.210:8443", true},
		{"[fd00::5]:8443", true},
		{"127.0.0.1:8443", true},
		{"169.254.169.254:80", false},
		{"[fe80::1]:8443", false},
		{"224.0.0.1:8443", false},
		{"0.0.0.0:8443", false},
	}
	for _, tt := range tests {
		err := refuseLinkLocal("tcp", tt.addr, nil)
		if (err == nil) != tt.ok {
			t.Errorf("refuseLinkLocal(%s) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
}

func TestTryCheckReachabilityOneAtATime(t *testing.T) {
	svc, _ := newTestService(t)
	svc.reachMu.Lock()
	_, err := svc.TryCheckReachability(context.Background())
	svc.reachMu.Unlock()
	if !errors.Is(err, ErrCheckRunning) {
		t.Fatalf("err = %v, want ErrCheckRunning", err)
	}
	if _, err := svc.TryCheckReachability(context.Background()); err != nil {
		t.Fatalf("second check: %v", err)
	}
}

func TestReloadEndpointsSANCap(t *testing.T) {
	svc, mgr := newTestService(t)
	var host string
	svc.SetEndpointResolver(func() Endpoints {
		return Endpoints{BaseURL: "https://" + host, AgentURL: "wss://" + host + ":8443/connect"}
	})
	var lastErr error
	for i := 0; i < 40 && lastErr == nil; i++ {
		host = fmt.Sprintf("dm%d.lan", i)
		_, lastErr = svc.ReloadEndpoints()
	}
	if lastErr == nil {
		t.Fatal("40 distinct agent hosts were all added to the cert")
	}
	if n := len(mgr.ServerCertSANs()); n > 32 {
		t.Errorf("cert carries %d SANs", n)
	}
}
