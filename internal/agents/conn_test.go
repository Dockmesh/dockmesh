package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAgent is a ConnectedAgent with nobody draining its send queue;
// tests read the queued frames directly, standing in for pump + agent.
func fakeAgent(caps ...string) (*ConnectedAgent, *atomic.Bool) {
	var closed atomic.Bool
	ag := newConnectedAgent("a1", "edge-1", func() { closed.Store(true) })
	ag.Hello = HelloPayload{ProtocolVersion: ProtocolVersion, Capabilities: caps}
	return ag, &closed
}

func nextFrame(t *testing.T, ag *ConnectedAgent) Frame {
	t.Helper()
	select {
	case f := <-ag.send:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame queued")
		return Frame{}
	}
}

func respondOK(ag *ConnectedAgent, id string) {
	payload, _ := json.Marshal(ResponseEnvelope{OK: true, Data: json.RawMessage(`{}`)})
	ag.deliverResponse(Frame{Type: FrameRes, ID: id, Payload: payload})
}

func TestSendAfterDisconnectDoesNotPanic(t *testing.T) {
	svc, _ := newTestService(t)
	ag, closed := fakeAgent(CapStreamCredit)
	svc.connected[ag.ID] = ag
	st, err := ag.OpenStream(context.Background(), "exec", "c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	nextFrame(t, ag)

	svc.Disconnect(ag.ID)
	if !closed.Load() {
		t.Fatal("disconnect left the socket open")
	}
	if svc.GetConnected(ag.ID) != nil {
		t.Fatal("disconnected agent still registered")
	}

	if _, err := ag.Request(context.Background(), Frame{Type: FrameReqContainerList}); !errors.Is(err, ErrAgentGone) {
		t.Fatalf("request after disconnect: %v", err)
	}
	if _, err := ag.OpenStream(context.Background(), "logs", "c1", nil); !errors.Is(err, ErrAgentGone) {
		t.Fatalf("open stream after disconnect: %v", err)
	}
	if err := st.WriteFrame([]byte("ls\n")); err == nil {
		t.Fatal("stream write after disconnect succeeded")
	}
	if err := st.WriteControl("resize", nil); err == nil {
		t.Fatal("stream control after disconnect succeeded")
	}
	ag.Send(Frame{Type: FrameServerPing})
	_ = st.Close()
	svc.Disconnect(ag.ID)
}

func TestRequestRoundTrip(t *testing.T) {
	ag, _ := fakeAgent()
	go func() {
		f := <-ag.send
		respondOK(ag, f.ID)
	}()
	if _, err := ag.Request(context.Background(), Frame{Type: FrameReqContainerList}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHonoursContextAndCancelsRemote(t *testing.T) {
	ag, _ := fakeAgent(CapRequestCancel)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ag.Request(ctx, Frame{Type: FrameReqStackDeploy})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("request ignored the caller's deadline")
	}
	req := nextFrame(t, ag)
	cancelFrame := nextFrame(t, ag)
	if cancelFrame.Type != FrameReqCancel || cancelFrame.ID != req.ID {
		t.Fatalf("got %+v, want req.cancel for %s", cancelFrame, req.ID)
	}
}

func TestRequestNoCancelForOldAgents(t *testing.T) {
	ag, _ := fakeAgent()
	ag.Hello = HelloPayload{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = ag.Request(ctx, Frame{Type: FrameReqContainerList})
	nextFrame(t, ag)
	select {
	case f := <-ag.send:
		t.Fatalf("old agent got %s", f.Type)
	default:
	}
	if !ag.UpdateRecommended() {
		t.Fatal("agent without protocol version should be told to update")
	}
}

func TestRequestEndsWhenAgentLeaves(t *testing.T) {
	ag, _ := fakeAgent()
	errc := make(chan error, 1)
	go func() {
		_, err := ag.Request(context.Background(), Frame{Type: FrameReqStackDeploy})
		errc <- err
	}()
	nextFrame(t, ag)
	ag.shutdown()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrAgentGone) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request kept waiting on a dead session")
	}
}

func TestEnqueueGivesUpOnStalledQueue(t *testing.T) {
	old := enqueueTimeout
	enqueueTimeout = 20 * time.Millisecond
	t.Cleanup(func() { enqueueTimeout = old })

	ag, _ := fakeAgent()
	for i := 0; i < cap(ag.send); i++ {
		ag.send <- Frame{}
	}
	_, err := ag.Request(context.Background(), Frame{Type: FrameReqContainerList})
	if !errors.Is(err, ErrSendQueueFull) {
		t.Fatalf("got %v, want queue full", err)
	}
}

func TestRequestTimeouts(t *testing.T) {
	for _, tc := range []struct {
		frame string
		min   time.Duration
		max   time.Duration
	}{
		{FrameReqContainerList, 10 * time.Second, time.Minute},
		{FrameReqStackDeploy, 10 * time.Minute, time.Hour},
		{FrameReqStackScale, 10 * time.Minute, time.Hour},
		{FrameReqContainerExecRun, 10 * time.Minute, time.Hour},
		{FrameReqAgentUpgrade, 10 * time.Minute, time.Hour},
	} {
		if d := requestTimeout(tc.frame); d < tc.min || d > tc.max {
			t.Errorf("%s: timeout %s outside [%s, %s]", tc.frame, d, tc.min, tc.max)
		}
	}
}

func TestMarkOnlineFailureLeavesNoEntry(t *testing.T) {
	svc, _ := newTestService(t)
	ag, _ := fakeAgent()
	_ = svc.db.Close()
	if err := svc.markOnline(context.Background(), ag, HelloPayload{}); err == nil {
		t.Fatal("markOnline succeeded on a closed database")
	}
	if svc.GetConnected(ag.ID) != nil {
		t.Fatal("failed markOnline left the agent registered")
	}
}

func TestMarkOfflineKeepsNewerSession(t *testing.T) {
	svc, _ := newTestService(t)
	old, _ := fakeAgent()
	cur, _ := fakeAgent()
	svc.connected[cur.ID] = cur
	svc.markOffline(old)
	if svc.GetConnected(cur.ID) != cur {
		t.Fatal("an old session's teardown removed the new one")
	}
}

func dataFrame(id string, b []byte) Frame {
	payload, _ := json.Marshal(StreamData{StreamID: id, Data: b})
	return Frame{Type: FrameStreamData, Payload: payload}
}

func closeFrame(id, msg string) Frame {
	payload, _ := json.Marshal(StreamClose{StreamID: id, Error: msg})
	return Frame{Type: FrameStreamClose, Payload: payload}
}

func controlFrame(id, op string, params map[string]any) Frame {
	payload, _ := json.Marshal(StreamControl{StreamID: id, Op: op, Params: params})
	return Frame{Type: FrameStreamControl, Payload: payload}
}

func TestStreamCreditBackpressure(t *testing.T) {
	ag, _ := fakeAgent(CapStreamCredit)
	st, err := ag.OpenStream(context.Background(), "volume_export", "", map[string]any{"volume": "v"})
	if err != nil {
		t.Fatal(err)
	}
	var open StreamOpen
	_ = json.Unmarshal(nextFrame(t, ag).Payload, &open)
	if CreditCount(map[string]any{"n": open.Params["window"]}) != StreamWindow {
		t.Fatalf("stream.open params %v lack the window", open.Params)
	}

	// A full window is buffered without loss.
	for i := 0; i < StreamWindow; i++ {
		ag.routeStreamFrame(dataFrame(st.ID, []byte{byte(i)}))
	}
	buf := make([]byte, 1)
	for i := 0; i < GrantBatch; i++ {
		if _, err := st.Read(buf); err != nil || buf[0] != byte(i) {
			t.Fatalf("read %d: %v %v", i, buf, err)
		}
	}
	f := nextFrame(t, ag)
	var sc StreamControl
	_ = json.Unmarshal(f.Payload, &sc)
	if f.Type != FrameStreamControl || sc.Op != StreamOpCredit || CreditCount(sc.Params) != GrantBatch {
		t.Fatalf("got %s %+v, want a credit grant of %d", f.Type, sc, GrantBatch)
	}
}

func TestStreamOverflowFailsLoudly(t *testing.T) {
	ag, _ := fakeAgent()
	st, err := ag.OpenStream(context.Background(), "volume_export", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	nextFrame(t, ag)
	for i := 0; i <= StreamWindow; i++ {
		ag.routeStreamFrame(dataFrame(st.ID, []byte{1}))
	}
	n, err := io.Copy(io.Discard, st)
	if !errors.Is(err, ErrStreamOverflow) {
		t.Fatalf("got %v after %d bytes, want overflow", err, n)
	}
	if n != StreamWindow {
		t.Fatalf("buffered data lost: got %d bytes", n)
	}
	if f := nextFrame(t, ag); f.Type != FrameStreamClose {
		t.Fatalf("agent not told to stop, got %s", f.Type)
	}
}

func TestStreamDeliversDataThenAgentError(t *testing.T) {
	ag, _ := fakeAgent(CapStreamCredit)
	st, _ := ag.OpenStream(context.Background(), "volume_import", "", nil)
	nextFrame(t, ag)
	ag.routeStreamFrame(dataFrame(st.ID, []byte("ab")))
	ag.routeStreamFrame(closeFrame(st.ID, "tar exit 2"))
	b, err := io.ReadAll(st)
	if string(b) != "ab" || err == nil || err.Error() != "tar exit 2" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestStreamCloseUnblocksReader(t *testing.T) {
	ag, _ := fakeAgent()
	st, _ := ag.OpenStream(context.Background(), "exec", "c1", nil)
	nextFrame(t, ag)
	done := make(chan error, 1)
	go func() {
		_, err := st.Read(make([]byte, 8))
		done <- err
	}()
	_ = st.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader stayed blocked after Close")
	}
	if f := nextFrame(t, ag); f.Type != FrameStreamClose {
		t.Fatalf("got %s, want stream.close", f.Type)
	}
}

func TestStreamDisconnectIsAnError(t *testing.T) {
	ag, _ := fakeAgent()
	st, _ := ag.OpenStream(context.Background(), "volume_export", "", nil)
	nextFrame(t, ag)
	ag.shutdown()
	ag.closeAllStreams()
	if _, err := st.Read(make([]byte, 8)); !errors.Is(err, ErrAgentGone) {
		t.Fatalf("got %v, want agent gone", err)
	}
}

func TestStreamWriteWaitsForCredit(t *testing.T) {
	ag, _ := fakeAgent(CapStreamCredit)
	st, _ := ag.OpenStream(context.Background(), "volume_import", "", nil)
	nextFrame(t, ag)
	t.Cleanup(ag.shutdown)
	go func() {
		for {
			select {
			case <-ag.send:
			case <-ag.done:
				return
			}
		}
	}()
	for i := 0; i < StreamWindow; i++ {
		if err := st.WriteFrame([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	wrote := make(chan error, 1)
	go func() { wrote <- st.WriteFrame([]byte{2}) }()
	select {
	case <-wrote:
		t.Fatal("write past the window did not wait for credit")
	case <-time.After(50 * time.Millisecond):
	}
	ag.routeStreamFrame(controlFrame(st.ID, StreamOpCredit, map[string]any{"n": 1}))
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("credit did not release the writer")
	}
}
