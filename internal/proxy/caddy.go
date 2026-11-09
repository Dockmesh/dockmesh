package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"reflect"
	"sync/atomic"
	"time"
)

const (
	// adminPort is the admin API's TCP port on containers created before
	// it moved to a unix socket; routes may still not point at it.
	adminPort       = 2019
	legacyAdminAddr = "127.0.0.1:2019"

	// AdminSocketDirName is the directory below the data dir that holds
	// the admin socket and is mounted into the proxy container.
	AdminSocketDirName = "caddy-admin"
	adminSocketName    = "admin.sock"
	adminMountDir      = "/run/dockmesh-caddy"
	adminHost          = "dockmesh-proxy"

	// Caddy runs as root and can't give the socket to Dockmesh's user, so
	// the socket is open and its 0700 directory decides who connects.
	adminListen = "unix/" + adminMountDir + "/" + adminSocketName + "|0666"

	// maxSocketPath is what fits in sockaddr_un.sun_path.
	maxSocketPath = 107
)

// adminOrigins are the only Host/Origin values Caddy's admin API accepts.
var (
	adminOrigins       = []string{adminHost}
	legacyAdminOrigins = []string{legacyAdminAddr, "localhost:2019"}
)

// adminEndpoint is where Caddy's admin API is reached.
type adminEndpoint struct {
	// url is the base URL; it also goes out as the Origin header.
	url string
	// socket is the unix socket to dial; empty dials url over TCP.
	socket string
}

var (
	// adminEP is the endpoint in use; nil until the socket dir is known.
	adminEP atomic.Pointer[adminEndpoint]
	// legacyEP reaches a container that still has its admin API on TCP.
	legacyEP = &adminEndpoint{url: "http://" + legacyAdminAddr}
)

// dialSocket refuses anything but a socket at the path: the directory is
// writable from inside the proxy container, so a compromised Caddy could
// otherwise point Dockmesh at another file through a symlink.
func dialSocket(ctx context.Context, path string) (net.Conn, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return nil, fmt.Errorf("%s is not a socket", path)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

func (e *adminEndpoint) client(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if e.socket != "" {
		socket := e.socket
		c.Transport = &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialSocket(ctx, socket)
			},
		}
	}
	return c
}

// adminDo sends a request to Caddy's admin API with the Origin header
// enforce_origin requires.
func adminDo(ctx context.Context, method, path string, body io.Reader, timeout time.Duration) (*http.Response, error) {
	return adminDoType(ctx, method, path, body, "application/json", timeout)
}

func adminDoType(ctx context.Context, method, path string, body io.Reader, contentType string, timeout time.Duration) (*http.Response, error) {
	ep := adminEP.Load()
	if ep == nil {
		return nil, errors.New("caddy admin socket is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.url+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Origin", ep.url)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	return ep.client(timeout).Do(req)
}

// errNotManagedCaddy means something answers on the admin address but
// not to the managed container's admin ID.
var errNotManagedCaddy = errors.New("the Caddy admin API that answered is not the container Dockmesh manages")

// verifyAdmin checks that the admin API answers to id, which only the
// managed container's config carries.
func verifyAdmin(ctx context.Context, id string) error {
	if !validAdminID(id) {
		return errNotManagedCaddy
	}
	resp, err := adminDo(ctx, http.MethodGet, "/id/"+id, nil, 2*time.Second)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return errNotManagedCaddy
	}
	return nil
}

// pushConfig POSTs the config to Caddy's admin API /load endpoint after
// checking it reaches the managed Caddy. Caddy replaces its entire
// running config atomically and saves it, so a restarted container
// (`--resume`) comes back with the same routes.
func pushConfig(ctx context.Context, cfg *caddyConfig) error {
	if cfg.Admin == nil {
		return errNotManagedCaddy
	}
	if err := verifyAdmin(ctx, cfg.Admin.ID); err != nil {
		return err
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	resp, err := adminDo(ctx, http.MethodPost, "/load", bytes.NewReader(body), 10*time.Second)
	if err != nil {
		return fmt.Errorf("caddy admin unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("caddy /load %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// runningConfig fetches the config Caddy is currently running.
func runningConfig(ctx context.Context) ([]byte, error) {
	resp, err := adminDo(ctx, http.MethodGet, "/config/", nil, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("caddy /config %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// sameConfig reports whether Caddy's running config (as returned by
// /config/) matches want, ignoring formatting and key order.
func sameConfig(running []byte, want *caddyConfig) bool {
	wantJSON, err := json.Marshal(want)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(running, &a) != nil || json.Unmarshal(wantJSON, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

// adminStatus pings the Caddy admin API and reports whether it's reachable.
func adminStatus(ctx context.Context) (bool, string) {
	resp, err := adminDo(ctx, http.MethodGet, "/config/", nil, 2*time.Second)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, ""
	}
	// Caddy exposes its version in the Server header of most responses.
	return true, resp.Header.Get("Server")
}

// adminReadyTimeout is a variable so tests don't wait for it.
var adminReadyTimeout = 15 * time.Second

// ensureAdmin waits for the managed Caddy's admin API to answer to id.
// Used right after starting the caddy container.
func ensureAdmin(ctx context.Context, id string) error {
	deadline := time.Now().Add(adminReadyTimeout)
	lastErr := errors.New("caddy admin API did not become ready")
	for time.Now().Before(deadline) {
		err := verifyAdmin(ctx, id)
		if err == nil {
			return nil
		}
		if errors.Is(err, errNotManagedCaddy) {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return lastErr
}
