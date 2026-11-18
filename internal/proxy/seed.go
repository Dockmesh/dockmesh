package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"sync"

	"github.com/dockmesh/dockmesh/internal/docker"
)

// ValidACMEEmail checks an ACME email the way the proxy settings do.
func ValidACMEEmail(email string) error {
	return Settings{ACMEEmail: email}.validate()
}

// SeedRoute stores an enabled route with an automatic certificate in a
// database no server runs on yet, for setup: the proxy pushes the stored
// routes when it starts.
func SeedRoute(ctx context.Context, db *sql.DB, host string, port int) error {
	if err := checkRouteHost(host); err != nil {
		return fmt.Errorf("%s: %w", host, err)
	}
	upstream := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if err := checkUpstream(upstream); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO proxy_routes (host, upstream, tls_mode, enabled, options, paths) VALUES (?, ?, 'auto', 1, '{}', '[]')`,
		host, upstream)
	return err
}

// BusyPorts returns the proxy ports (80, 443) some program listens on.
func BusyPorts() ([]int, error) {
	counts, err := readListeners()
	if err != nil {
		return nil, err
	}
	var out []int
	for _, p := range proxyPorts {
		if counts[p] > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

var pullMu sync.Mutex

// PullDefaultImage pulls the Caddy image a new install starts with, unless
// Docker has it already.
func PullDefaultImage(ctx context.Context, dockerCli *docker.Client) error {
	pullMu.Lock()
	defer pullMu.Unlock()
	s := &Service{docker: dockerCli}
	return s.ensureImage(ctx, DefaultProxyImage)
}
