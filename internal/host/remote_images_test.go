package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// imageAgent answers image streams with body, or fails them with closeErr,
// and reports each stream.open it received on opens.
func imageAgent(t *testing.T, caps []string, body, closeErr string) (*RemoteHost, <-chan agents.StreamOpen) {
	t.Helper()
	opens := make(chan agents.StreamOpen, 4)
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: caps},
		func(f agents.Frame, reply func(agents.Frame)) {
			if f.Type != agents.FrameStreamOpen {
				return
			}
			var open agents.StreamOpen
			_ = json.Unmarshal(f.Payload, &open)
			opens <- open
			if body != "" {
				d, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: []byte(body)})
				reply(agents.Frame{Type: agents.FrameStreamData, Payload: d})
			}
			c, _ := json.Marshal(agents.StreamClose{StreamID: open.StreamID, Error: closeErr})
			reply(agents.Frame{Type: agents.FrameStreamClose, Payload: c})
		})
	t.Cleanup(stop)
	return NewRemote("a1", "edge-1", &fakeLive{ag: ag}), opens
}

func TestRemoteImageStreams(t *testing.T) {
	caps := []string{agents.CapImageTransfer}
	ctx := context.Background()

	t.Run("pull carries the credential of this request", func(t *testing.T) {
		h, opens := imageAgent(t, caps, "{\"status\":\"Pulling\"}\n", "")
		rc, err := h.PullImage(ctx, "reg.test/app:1", "c2VjcmV0")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil || string(got) != "{\"status\":\"Pulling\"}\n" {
			t.Fatalf("read %q, %v", got, err)
		}
		open := <-opens
		if open.Kind != "image_pull" || open.Params["image"] != "reg.test/app:1" || open.Params["registry_auth"] != "c2VjcmV0" {
			t.Fatalf("stream.open = %+v", open)
		}
	})

	t.Run("export streams the archive", func(t *testing.T) {
		h, opens := imageAgent(t, caps, "tar-bytes", "")
		rc, err := h.ExportImage(ctx, "nginx:1")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if got, err := io.ReadAll(rc); err != nil || string(got) != "tar-bytes" {
			t.Fatalf("read %q, %v", got, err)
		}
		if open := <-opens; open.Kind != "image_export" || open.Params["image"] != "nginx:1" {
			t.Fatalf("stream.open = %+v", open)
		}
	})

	t.Run("an agent failure is the read error", func(t *testing.T) {
		h, _ := imageAgent(t, caps, "", "pull access denied")
		rc, err := h.PullImage(ctx, "private/app", "")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if _, err := io.ReadAll(rc); err == nil || err.Error() != "pull access denied" {
			t.Fatalf("read error = %v", err)
		}
	})
}

func TestOlderAgentRefusesImageTransfer(t *testing.T) {
	h, opens := imageAgent(t, []string{agents.CapVolumeManage}, "", "")
	ctx := context.Background()
	if _, err := h.PullImage(ctx, "nginx", ""); !errors.Is(err, ErrAgentNoImageTransfer) {
		t.Fatalf("pull: %v", err)
	}
	if _, err := h.ExportImage(ctx, "nginx"); !errors.Is(err, ErrAgentNoImageTransfer) {
		t.Fatalf("export: %v", err)
	}
	select {
	case open := <-opens:
		t.Fatalf("an older agent was sent %+v", open)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestImageStreamEndsWithTheRequest(t *testing.T) {
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: []string{agents.CapImageTransfer}},
		func(agents.Frame, func(agents.Frame)) {})
	t.Cleanup(stop)
	h := NewRemote("a1", "edge-1", &fakeLive{ag: ag})
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := h.PullImage(ctx, "nginx", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	done := make(chan error, 1)
	go func() {
		_, err := rc.Read(make([]byte, 16))
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read succeeded after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read still blocked after the request ended")
	}
}
