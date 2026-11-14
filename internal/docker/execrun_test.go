package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

type fakeExecClient struct {
	client.ExecAPIClient
	stream []byte
	exit   int
	// running is how many inspects still report the exec as running.
	running  int
	inspects int
	// block makes the output stream wait until the connection is closed.
	block bool
}

func (f *fakeExecClient) ExecCreate(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
	return client.ExecCreateResult{ID: "e1"}, nil
}

func (f *fakeExecClient) ExecAttach(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error) {
	c1, c2 := net.Pipe()
	if f.block {
		return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: c1, Reader: bufio.NewReader(c1)}}, nil
	}
	_ = c2.Close()
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: c1, Reader: bufio.NewReader(bytes.NewReader(f.stream))}}, nil
}

func (f *fakeExecClient) ExecInspect(context.Context, string, client.ExecInspectOptions) (client.ExecInspectResult, error) {
	f.inspects++
	if f.inspects <= f.running {
		return client.ExecInspectResult{Running: true}, nil
	}
	return client.ExecInspectResult{ExitCode: f.exit}, nil
}

// muxed builds a multiplexed exec stream: an 8-byte header (stream type,
// three zero bytes, big-endian length) before every frame.
func muxed(parts ...any) []byte {
	var buf bytes.Buffer
	for i := 0; i < len(parts); i += 2 {
		typ := stdcopy.Stdout
		if parts[i] == "err" {
			typ = stdcopy.Stderr
		}
		payload := parts[i+1].(string)
		hdr := [8]byte{byte(typ)}
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload))) //nolint:gosec // test payloads are tiny
		buf.Write(hdr[:])
		buf.WriteString(payload)
	}
	return buf.Bytes()
}

func TestRunExecDemultiplexes(t *testing.T) {
	tests := []struct {
		name   string
		stream []byte
		max    int64
		want   string
	}{
		{"stdout and stderr interleaved", muxed("out", "checking\n", "err", "pg_dump: failed\n", "out", "done\n"), 1 << 20, "checking\npg_dump: failed\ndone\n"},
		{"capped", muxed("out", "0123456789", "err", "abcdef"), 12, "0123456789ab"},
		{"no output", nil, 1 << 20, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code, err := RunExec(context.Background(), &fakeExecClient{stream: tt.stream, exit: 3}, "c1", []string{"sh"}, tt.max)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want || code != 3 {
				t.Fatalf("got %q exit %d, want %q exit 3", got, code, tt.want)
			}
		})
	}
}

func TestRunExecWaitsForExit(t *testing.T) {
	defer func(d time.Duration) { execExitPoll = d }(execExitPoll)
	execExitPoll = time.Millisecond
	big := strings.Repeat("x", 64)
	f := &fakeExecClient{stream: muxed("out", big, "out", big, "err", "failed"), exit: 2, running: 3}
	got, code, err := RunExec(context.Background(), f, "c1", []string{"sh"}, 16)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != big[:16] || code != 2 || f.inspects != 4 {
		t.Fatalf("got %q exit %d after %d inspects, want the first 16 bytes, exit 2 after 4", got, code, f.inspects)
	}
}

func TestRunExecStopsOnContextEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := RunExec(ctx, &fakeExecClient{block: true}, "c1", []string{"yes"}, 16)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunExec kept reading after the deadline")
	}
}
