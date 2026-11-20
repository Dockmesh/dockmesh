package swarmread

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// logDaemon is a manager with one service "web" whose log stream is
// body; tasks are read anew on every TaskList call.
type logDaemon struct {
	manager bool
	tty     bool
	body    []byte
	block   bool

	mu        sync.Mutex
	tasks     []swarm.Task
	taskReads int
	// afterFirstRead joins the task list from the second read on.
	afterFirstRead []swarm.Task
	opts           client.ServiceLogsOptions
}

func (d *logDaemon) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	sw := swarm.Info{LocalNodeState: swarm.LocalNodeStateActive, ControlAvailable: d.manager}
	return client.SystemInfoResult{Info: system.Info{Swarm: sw}}, nil
}

func (d *logDaemon) NodeList(context.Context, client.NodeListOptions) (client.NodeListResult, error) {
	return client.NodeListResult{Items: []swarm.Node{
		{ID: "n1", Description: swarm.NodeDescription{Hostname: "m1"}},
		{ID: "n2", Description: swarm.NodeDescription{Hostname: "w1"}},
	}}, nil
}

func (d *logDaemon) ServiceList(context.Context, client.ServiceListOptions) (client.ServiceListResult, error) {
	return client.ServiceListResult{}, nil
}

func (d *logDaemon) ServiceInspect(_ context.Context, id string, _ client.ServiceInspectOptions) (client.ServiceInspectResult, error) {
	if id != "s1" && id != "web" {
		return client.ServiceInspectResult{}, errNotFound{}
	}
	spec := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{TTY: d.tty}}}
	return client.ServiceInspectResult{Service: swarm.Service{ID: "s1", Spec: spec}}, nil
}

func (d *logDaemon) TaskList(context.Context, client.TaskListOptions) (client.TaskListResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.taskReads++
	if d.taskReads == 2 {
		d.tasks = append(d.tasks, d.afterFirstRead...)
	}
	return client.TaskListResult{Items: append([]swarm.Task(nil), d.tasks...)}, nil
}

func (d *logDaemon) NetworkList(context.Context, client.NetworkListOptions) (client.NetworkListResult, error) {
	return client.NetworkListResult{}, nil
}

func (d *logDaemon) ServiceLogs(ctx context.Context, _ string, o client.ServiceLogsOptions) (client.ServiceLogsResult, error) {
	d.mu.Lock()
	d.opts = o
	d.mu.Unlock()
	if d.block {
		pr, pw := io.Pipe()
		go func() {
			<-ctx.Done()
			pw.CloseWithError(ctx.Err())
		}()
		return pr, nil
	}
	return io.NopCloser(bytes.NewReader(d.body)), nil
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }
func (errNotFound) NotFound()     {}

func muxed(t *testing.T, frames ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, f := range frames {
		hdr := [8]byte{byte(stdcopy.Stdout)}
		if f[0] == "stderr" {
			hdr[0] = byte(stdcopy.Stderr)
		}
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(f[1])))
		buf.Write(hdr[:])
		buf.WriteString(f[1])
	}
	return buf.Bytes()
}

const (
	detailsT1 = "com.docker.swarm.node.id=n1,com.docker.swarm.service.id=s1,com.docker.swarm.task.id=t1"
	detailsT2 = "com.docker.swarm.node.id=n2,com.docker.swarm.service.id=s1,com.docker.swarm.task.id=t2"
)

func collect(t *testing.T, d *logDaemon, req agents.SwarmLogsReq) ([]agents.SwarmLogLine, error) {
	t.Helper()
	var lines []agents.SwarmLogLine
	err := ServiceLogs(context.Background(), d, req, func(l agents.SwarmLogLine) error {
		lines = append(lines, l)
		return nil
	})
	return lines, err
}

func TestServiceLogsPrefixesEveryTask(t *testing.T) {
	d := &logDaemon{manager: true,
		tasks: []swarm.Task{{ID: "t1", Slot: 1, NodeID: "n1"}, {ID: "t2", Slot: 2, NodeID: "n2"}},
		body: muxed(t,
			[2]string{"stdout", "2026-11-20T10:00:00.5Z " + detailsT1 + " hello from one\n"},
			[2]string{"stderr", "2026-11-20T10:00:01Z " + detailsT2 + ",env_DB_PASSWORD=hunter2,tag=hunter2 boom\n"},
			[2]string{"stdout", "2026-11-20T10:00:02Z " + detailsT2 + " no newline at the end"},
		)}
	lines, err := collect(t, d, agents.SwarmLogsReq{Service: "web", Tail: 7, Since: "1700000000", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ task, node, stream, msg string }{
		{"web.1", "m1", "stdout", "hello from one"},
		{"web.2", "w1", "stderr", "boom"},
		{"web.2", "w1", "stdout", "no newline at the end"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines: %+v", len(lines), lines)
	}
	for i, w := range want {
		l := lines[i]
		if l.Task != w.task || l.Node != w.node || l.Stream != w.stream || l.Message != w.msg || l.Time.IsZero() {
			t.Errorf("line %d = %+v, want %+v", i, l, w)
		}
	}
	for _, l := range lines {
		if strings.Contains(l.Message+l.Task+l.Node, "hunter2") {
			t.Fatalf("log driver attribute leaked: %+v", l)
		}
	}
	if o := d.opts; !o.Details || !o.Timestamps || !o.Follow || o.Tail != "7" || o.Since != "1700000000" || !o.ShowStderr || !o.ShowStdout {
		t.Fatalf("options %+v", o)
	}
}

func TestServiceLogsTTYAndLongLines(t *testing.T) {
	long := strings.Repeat("x", agents.MaxSwarmLogLine+5000)
	d := &logDaemon{manager: true, tty: true, tasks: []swarm.Task{{ID: "t1", Slot: 1, NodeID: "n1"}},
		body: []byte("2026-11-20T10:00:00Z " + detailsT1 + " " + long + "\r\n2026-11-20T10:00:00Z " + detailsT1 + " after\n")}
	lines, err := collect(t, d, agents.SwarmLogsReq{Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	if !lines[0].Truncated || len(lines[0].Message) > agents.MaxSwarmLogLine {
		t.Fatalf("long line: truncated=%v len=%d", lines[0].Truncated, len(lines[0].Message))
	}
	if lines[1].Message != "after" || lines[1].Truncated || lines[1].Stream != "stdout" {
		t.Fatalf("line after the long one: %+v", lines[1])
	}
}

// A task that started after the stream opened is named once the task list
// is read again; lines without a task id are dropped.
func TestServiceLogsNewTaskAndJunk(t *testing.T) {
	d := &logDaemon{manager: true, tasks: []swarm.Task{{ID: "t1", Slot: 1, NodeID: "n1"}},
		body: muxed(t,
			[2]string{"stdout", "garbage\n"},
			[2]string{"stdout", "2026-11-20T10:00:00Z com.docker.swarm.node.id=n1 no task\n"},
			[2]string{"stdout", "2026-11-20T10:00:00Z " + detailsT2 + " new task\n"},
		)}
	d.afterFirstRead = []swarm.Task{{ID: "t2", Slot: 3, NodeID: "n2"}}
	lines, err := collect(t, d, agents.SwarmLogsReq{Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Task != "web.3" || lines[0].Node != "w1" {
		t.Fatalf("lines %+v", lines)
	}
}

func TestServiceLogsRefusals(t *testing.T) {
	tests := []struct {
		name    string
		d       *logDaemon
		service string
		want    error
	}{
		{"worker", &logDaemon{}, "web", agents.ErrNotSwarmManager},
		{"unknown service", &logDaemon{manager: true}, "nope", agents.ErrSwarmServiceNotFound},
		{"bad ref", &logDaemon{manager: true}, "../x", errInvalidID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := collect(t, tt.d, agents.SwarmLogsReq{Service: tt.service})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
		})
	}
}

// Cancelling the caller's context ends a follow stream without an error.
func TestServiceLogsCancel(t *testing.T) {
	d := &logDaemon{manager: true, block: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServiceLogs(ctx, d, agents.SwarmLogsReq{Service: "web", Follow: true}, func(agents.SwarmLogLine) error { return nil })
	}()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("err %v", err)
	}
}

func TestLineWriterSplits(t *testing.T) {
	var got []string
	w := &lineWriter{line: func(b []byte, _ bool) error {
		got = append(got, string(b))
		return nil
	}}
	for _, p := range []string{"a", "b\nc", "\n\nd"} {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "ab|c||d" {
		t.Fatalf("got %q", got)
	}
}
