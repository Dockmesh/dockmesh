package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/gorilla/websocket"
)

// wsPair connects a client websocket (the agent side) to a test server
// whose received frames arrive on the returned channel.
func wsPair(t *testing.T) (*websocket.Conn, <-chan agents.Frame) {
	t.Helper()
	frames := make(chan agents.Frame, 256)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			var f agents.Frame
			_ = json.Unmarshal(raw, &f)
			frames <- f
		}
	}))
	t.Cleanup(srv.Close)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	t.Cleanup(func() { conn.Close() })
	return conn, frames
}

func recvFrame(t *testing.T, frames <-chan agents.Frame) agents.Frame {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame received")
		return agents.Frame{}
	}
}

func TestReconnectDelay(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	if d := reconnectDelay(0, r); d != 0 {
		t.Fatalf("first retry waits %s, want immediate", d)
	}
	for _, tc := range []struct {
		attempt int
		base    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {4, 8 * time.Second}, {7, maxBackoff}, {50, maxBackoff}} {
		seen := map[time.Duration]bool{}
		for i := 0; i < 20; i++ {
			d := reconnectDelay(tc.attempt, r)
			lo, hi := time.Duration(float64(tc.base)*0.8), time.Duration(float64(tc.base)*1.2)
			if d < lo || d > hi {
				t.Fatalf("attempt %d: %s outside [%s, %s]", tc.attempt, d, lo, hi)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Fatalf("attempt %d: no jitter", tc.attempt)
		}
	}
}

func TestCancelRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	trackRequest("r1", cancel)
	cancelRequest("r1")
	if ctx.Err() == nil {
		t.Fatal("req.cancel did not cancel the handler context")
	}
	cancelRequest("unknown")
}

func newReg(t *testing.T, window int) *streamReg {
	t.Helper()
	params := map[string]any{}
	if window > 0 {
		params["window"] = float64(window)
	}
	reg := registerStream(context.Background(), agents.StreamOpen{StreamID: t.Name(), Params: params})
	t.Cleanup(func() { closeStream(reg.id) })
	return reg
}

func TestPumpOutputWaitsForCredit(t *testing.T) {
	conn, frames := wsPair(t)
	reg := newReg(t, 2)
	done := make(chan error, 1)
	go func() { done <- reg.pumpOutput(conn, bytes.NewReader([]byte("abc")), 1) }()
	recvFrame(t, frames)
	recvFrame(t, frames)
	select {
	case f := <-frames:
		t.Fatalf("sent %s past the window", f.Type)
	case <-time.After(50 * time.Millisecond):
	}
	deliverStreamControl(reg.id, agents.StreamOpCredit, map[string]any{"n": float64(1)})
	recvFrame(t, frames)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPumpInputDrainsBeforeEOFAndGrantsCredit(t *testing.T) {
	conn, frames := wsPair(t)
	reg := newReg(t, agents.StreamWindow)
	for i := 0; i < agents.GrantBatch; i++ {
		deliverStreamInput(conn, reg.id, []byte{'x'})
	}
	deliverStreamControl(reg.id, agents.StreamOpEOF, nil)
	var out bytes.Buffer
	if err := reg.pumpInput(conn, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != agents.GrantBatch {
		t.Fatalf("wrote %d bytes, want %d", out.Len(), agents.GrantBatch)
	}
	f := recvFrame(t, frames)
	var sc agents.StreamControl
	_ = json.Unmarshal(f.Payload, &sc)
	if sc.Op != agents.StreamOpCredit || agents.CreditCount(sc.Params) != agents.GrantBatch {
		t.Fatalf("got %s %+v, want a credit grant", f.Type, sc)
	}
}

func TestStreamInputOverflowFailsStream(t *testing.T) {
	conn, frames := wsPair(t)
	reg := newReg(t, 0)
	for i := 0; i <= agents.StreamWindow; i++ {
		deliverStreamInput(conn, reg.id, []byte{'x'})
	}
	f := recvFrame(t, frames)
	var sc agents.StreamClose
	_ = json.Unmarshal(f.Payload, &sc)
	if f.Type != agents.FrameStreamClose || sc.Error == "" {
		t.Fatalf("got %s %+v, want a failed stream.close", f.Type, sc)
	}
	if reg.ctx.Err() == nil {
		t.Fatal("overflowed stream still running")
	}
}

// agentFiles enrolls a fake agent against a real PKI and writes its
// files, returning the store and the manager that issued them.
func agentFiles(t *testing.T, validity time.Duration) (*certStore, *pki.Manager) {
	t.Helper()
	dir := t.TempDir()
	mgr, err := pki.New(filepath.Join(dir, "server"), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _, err := mgr.IssueClientCert("agent-1", validity)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, caPath := filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key"), filepath.Join(dir, "ca.crt")
	for p, b := range map[string][]byte{certPath: certPEM, keyPath: keyPEM, caPath: mgr.CACertPEM()} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := newCertStore(certPath, keyPath, caPath)
	if err != nil {
		t.Fatal(err)
	}
	return store, mgr
}

func TestRenewCertOverSession(t *testing.T) {
	store, mgr := agentFiles(t, 10*24*time.Hour)
	conn, frames := wsPair(t)

	errc := make(chan error, 1)
	go func() { errc <- renewCert(context.Background(), conn, store) }()
	f := recvFrame(t, frames)
	if f.Type != agents.FrameAgentCertRenew {
		t.Fatalf("got %s", f.Type)
	}
	var req agents.CertRenewReq
	_ = json.Unmarshal(f.Payload, &req)
	certPEM, _, _, err := mgr.SignClientCSR([]byte(req.CSR), "agent-1", 365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(agents.CertRenewRes{ClientCert: string(certPEM)})
	env, _ := json.Marshal(agents.ResponseEnvelope{OK: true, Data: data})
	// Stands in for the read loop routing the server's res frame.
	deliverResponse(agents.Frame{Type: agents.FrameRes, ID: f.ID, Payload: env})

	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if time.Until(store.notAfter()) < 300*24*time.Hour {
		t.Fatalf("store still holds the old cert (expires %s)", store.notAfter())
	}
	if _, err := tls.LoadX509KeyPair(store.certPath, store.keyPath); err != nil {
		t.Fatalf("renewed pair on disk: %v", err)
	}
	if fileExists(store.certPath+".next") || fileExists(store.keyPath+".next") {
		t.Fatal(".next files left behind")
	}
}

func TestCheckRenewedCertRejectsForeignCerts(t *testing.T) {
	store, mgr := agentFiles(t, time.Hour)
	_, otherMgr := agentFiles(t, time.Hour)
	old := store.cur.Load().Leaf

	for name, tc := range map[string]struct {
		issuer *pki.Manager
		cn     string
	}{
		"other agent": {mgr, "agent-2"},
		"foreign CA":  {otherMgr, "agent-1"},
	} {
		certPEM, keyPEM, _, err := tc.issuer.IssueClientCert(tc.cn, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkRenewedCert(certPEM, keyPEM, old, store.roots); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFinishPendingRenewal(t *testing.T) {
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	for _, tc := range []struct {
		name              string
		keyNext, certNext bool
		wantKey, wantCert string
	}{
		{"nothing pending", false, false, "old-key", "old-cert"},
		{"lone key is an unfinished write", true, false, "old-key", "old-cert"},
		{"both written", true, true, "new-key", "new-cert"},
		{"key already moved", false, true, "old-key", "new-cert"},
	} {
		dir := t.TempDir()
		certPath, keyPath := filepath.Join(dir, "agent.crt"), filepath.Join(dir, "agent.key")
		write(certPath, "old-cert")
		write(keyPath, "old-key")
		if tc.keyNext {
			write(keyPath+".next", "new-key")
		}
		if tc.certNext {
			write(certPath+".next", "new-cert")
		}
		if err := finishPendingRenewal(certPath, keyPath); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if read(keyPath) != tc.wantKey || read(certPath) != tc.wantCert {
			t.Errorf("%s: key %q cert %q", tc.name, read(keyPath), read(certPath))
		}
		if fileExists(keyPath+".next") || fileExists(certPath+".next") {
			t.Errorf("%s: .next files left", tc.name)
		}
	}
}

func TestCertStoreServesSwappedCert(t *testing.T) {
	store, mgr := agentFiles(t, time.Hour)
	cfg := store.tlsConfig()
	first, _ := cfg.GetClientCertificate(nil)

	certPEM, keyPEM, _, err := mgr.IssueClientCert("agent-1", 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(store.certPath, certPEM, 0o600)
	_ = os.WriteFile(store.keyPath, keyPEM, 0o600)
	if err := store.load(); err != nil {
		t.Fatal(err)
	}
	second, _ := cfg.GetClientCertificate(nil)
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("tls config kept serving the old cert")
	}
	block, _ := pem.Decode(certPEM)
	leaf, _ := x509.ParseCertificate(block.Bytes)
	if !store.notAfter().Equal(leaf.NotAfter) {
		t.Fatal("notAfter not updated")
	}
}
