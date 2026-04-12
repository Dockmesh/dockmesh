package proxy

import "context"

// Proxy is the reverse-proxy control interface. Phase 2 wires this to Caddy's admin API.
type Proxy interface {
	Upsert(ctx context.Context, route Route) error
	Delete(ctx context.Context, host string) error
}

type Route struct {
	Host     string
	Upstream string
	TLS      bool
}

// TODO(phase2): implement against http://localhost:2019 (Caddy admin API).
type Stub struct{}

func (Stub) Upsert(ctx context.Context, route Route) error { return nil }
func (Stub) Delete(ctx context.Context, host string) error { return nil }
