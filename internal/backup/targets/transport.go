package targets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	dialTimeout           = 15 * time.Second
	tlsHandshakeTimeout   = 15 * time.Second
	responseHeaderTimeout = 10 * time.Minute
	connIdleTimeout       = 90 * time.Second
	transferIdleTimeout   = 5 * time.Minute
)

// newTransferTransport returns an HTTP transport for moving whole archives.
// There is deliberately no overall client timeout: a multi-GiB upload takes as
// long as the link needs. Stalls are caught by the dial, TLS and header
// timeouts and by an idle deadline on every read and write of the connection.
func newTransferTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &idleConn{Conn: c, idle: transferIdleTimeout}, nil
	}
	t.TLSHandshakeTimeout = tlsHandshakeTimeout
	t.ResponseHeaderTimeout = responseHeaderTimeout
	t.IdleConnTimeout = connIdleTimeout
	return t
}

// idleConn pushes the deadline forward on every read and write, so a
// transfer that keeps making progress never times out but a stalled one does.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// dialTCP opens a connection that fails fast on an unreachable host and
// notices a dead peer through keepalives.
func dialTCP(addr string) (net.Conn, error) {
	c, err := (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &idleConn{Conn: c, idle: transferIdleTimeout}, nil
}

// transferTransportWithCA is newTransferTransport trusting an extra PEM CA
// bundle on top of the system roots; empty pem means system roots only.
func transferTransportWithCA(pem string) (*http.Transport, error) {
	t := newTransferTransport()
	if strings.TrimSpace(pem) == "" {
		return t, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("ca_cert holds no valid PEM certificate")
	}
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return t, nil
}
