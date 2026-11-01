package docker

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type fakeExecClient struct {
	client.ContainerAPIClient
	stream []byte
	exit   int
}

func (f *fakeExecClient) ContainerExecCreate(context.Context, string, container.ExecOptions) (container.ExecCreateResponse, error) {
	return container.ExecCreateResponse{ID: "e1"}, nil
}

func (f *fakeExecClient) ContainerExecAttach(context.Context, string, container.ExecStartOptions) (types.HijackedResponse, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return types.HijackedResponse{Conn: c1, Reader: bufio.NewReader(bytes.NewReader(f.stream))}, nil
}

func (f *fakeExecClient) ContainerExecInspect(context.Context, string) (container.ExecInspect, error) {
	return container.ExecInspect{ExitCode: f.exit}, nil
}

func muxed(parts ...any) []byte {
	var buf bytes.Buffer
	out, errw := stdcopy.NewStdWriter(&buf, stdcopy.Stdout), stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	for i := 0; i < len(parts); i += 2 {
		w := out
		if parts[i] == "err" {
			w = errw
		}
		_, _ = w.Write([]byte(parts[i+1].(string)))
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
