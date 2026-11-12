package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

type fakeExecClient struct {
	client.ExecAPIClient
	stream []byte
	exit   int
}

func (f *fakeExecClient) ExecCreate(context.Context, string, client.ExecCreateOptions) (client.ExecCreateResult, error) {
	return client.ExecCreateResult{ID: "e1"}, nil
}

func (f *fakeExecClient) ExecAttach(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: c1, Reader: bufio.NewReader(bytes.NewReader(f.stream))}}, nil
}

func (f *fakeExecClient) ExecInspect(context.Context, string, client.ExecInspectOptions) (client.ExecInspectResult, error) {
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
