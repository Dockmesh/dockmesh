package swarmread

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// LogsAPI is the part of the Docker client ServiceLogs uses.
type LogsAPI interface {
	API
	ServiceLogs(ctx context.Context, serviceID string, options client.ServiceLogsOptions) (client.ServiceLogsResult, error)
}

// namesRefresh is the shortest gap between two task/node list reads when
// a line names a task that wasn't known yet (a new or rescheduled task).
const namesRefresh = 5 * time.Second

// WriteServiceLogs writes the logs of every task of req.Service to w as
// SwarmLogLine NDJSON until the daemon ends the stream, w fails or ctx is
// done. It is refused unless the local daemon is a Swarm manager.
func WriteServiceLogs(ctx context.Context, cli LogsAPI, req agents.SwarmLogsReq, w io.Writer) error {
	enc := json.NewEncoder(w)
	return ServiceLogs(ctx, cli, req, func(l agents.SwarmLogLine) error { return enc.Encode(l) })
}

// ServiceLogs calls emit for every log line of req.Service's tasks.
func ServiceLogs(ctx context.Context, cli LogsAPI, req agents.SwarmLogsReq, emit func(agents.SwarmLogLine) error) error {
	if !ValidRef(req.Service) {
		return errInvalidID
	}
	setup, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if _, err := requireManager(setup, cli); err != nil {
		return err
	}
	res, err := cli.ServiceInspect(setup, req.Service, client.ServiceInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return agents.ErrSwarmServiceNotFound
	}
	if err != nil {
		return err
	}
	svc := res.Service
	names := &taskNames{cli: cli, service: svc}
	if err := names.refresh(setup); err != nil {
		return err
	}
	opts := client.ServiceLogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: req.Follow,
		Tail: strconv.Itoa(req.Tail), Since: req.Since,
		Timestamps: true, Details: true,
	}
	rc, err := cli.ServiceLogs(ctx, svc.ID, opts)
	if err != nil {
		return err
	}
	defer rc.Close()
	stop := context.AfterFunc(ctx, func() { _ = rc.Close() })
	defer stop()

	out := func(stream string) *lineWriter {
		return &lineWriter{line: func(b []byte, truncated bool) error {
			l, ok := parseLogLine(b)
			if !ok {
				return nil
			}
			l.Stream, l.Truncated = stream, truncated
			names.fill(ctx, &l)
			if l, ok = agents.SanitizeSwarmLogLine(l); !ok {
				return nil
			}
			return emit(l)
		}}
	}
	stdout, stderr := out("stdout"), out("stderr")
	if spec := svc.Spec.TaskTemplate.ContainerSpec; spec != nil && spec.TTY {
		_, err = io.Copy(stdout, rc)
	} else {
		_, err = stdcopy.StdCopy(stdout, stderr, rc)
	}
	if ferr := stdout.flush(); err == nil {
		err = ferr
	}
	if ferr := stderr.flush(); err == nil {
		err = ferr
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// parseLogLine splits "<timestamp> <details> <message>" as dockerd sends
// it with timestamps and details on. Of the details only the node and task
// ids are kept: a log driver's labels/env options add attributes there,
// and those may carry values that must not leave the host.
func parseLogLine(b []byte) (agents.SwarmLogLine, bool) {
	ts, rest, ok := bytes.Cut(b, []byte(" "))
	if !ok {
		return agents.SwarmLogLine{}, false
	}
	details, msg, _ := bytes.Cut(rest, []byte(" "))
	var l agents.SwarmLogLine
	if t, err := time.Parse(time.RFC3339Nano, string(ts)); err == nil {
		l.Time = t
	}
	for kv := range strings.SplitSeq(string(details), ",") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "com.docker.swarm.task.id", "com.docker.swarm.node.id":
			if u, err := url.QueryUnescape(v); err == nil {
				v = u
			}
			if k == "com.docker.swarm.task.id" {
				l.TaskID = v
			} else {
				l.NodeID = v
			}
		}
	}
	if l.TaskID == "" {
		return agents.SwarmLogLine{}, false
	}
	l.Message = string(bytes.TrimSuffix(msg, []byte("\r")))
	return l, true
}

// taskNames maps task ids to "<service>.<slot>" and node ids to host
// names. A line naming an unknown task re-reads the lists, at most once
// per namesRefresh; until then the line carries the service name only.
type taskNames struct {
	cli     API
	service swarm.Service

	mu    sync.Mutex
	tasks map[string]string
	nodes map[string]string
	last  time.Time
}

func (n *taskNames) refresh(ctx context.Context) error {
	tasks, err := n.cli.TaskList(ctx, client.TaskListOptions{Filters: client.Filters{}.Add("service", n.service.ID)})
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	nodes, err := n.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	tm := make(map[string]string, len(tasks.Items))
	for _, t := range tasks.Items {
		suffix := strconv.Itoa(t.Slot)
		if t.Slot == 0 {
			suffix = t.NodeID
		}
		tm[t.ID] = n.service.Spec.Name + "." + suffix
	}
	nm := make(map[string]string, len(nodes.Items))
	for _, nd := range nodes.Items {
		nm[nd.ID] = nd.Description.Hostname
	}
	n.mu.Lock()
	n.tasks, n.nodes = tm, nm
	n.mu.Unlock()
	return nil
}

func (n *taskNames) fill(ctx context.Context, l *agents.SwarmLogLine) {
	n.mu.Lock()
	task, known := n.tasks[l.TaskID]
	stale := time.Since(n.last) >= namesRefresh
	if !known && stale {
		n.last = time.Now()
	}
	n.mu.Unlock()
	if !known && stale {
		rctx, cancel := context.WithTimeout(ctx, Timeout)
		_ = n.refresh(rctx)
		cancel()
		n.mu.Lock()
		task, known = n.tasks[l.TaskID]
		n.mu.Unlock()
	}
	if !known {
		task = n.service.Spec.Name
	}
	l.Task = task
	n.mu.Lock()
	l.Node = n.nodes[l.NodeID]
	n.mu.Unlock()
}

// maxRawLine bounds one buffered line: the longest kept message plus room
// for the timestamp and the details in front of it, which grow with the
// attributes a log driver adds.
const maxRawLine = 2 * agents.MaxSwarmLogLine

// lineWriter splits what it is written into lines and hands each to line,
// cut to maxRawLine with the rest of an over-long line dropped.
type lineWriter struct {
	line     func(b []byte, truncated bool) error
	buf      []byte
	dropping bool
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !w.dropping {
			room := maxRawLine - len(w.buf)
			if len(chunk) > room {
				w.buf = append(w.buf, chunk[:room]...)
				w.dropping = true
			} else {
				w.buf = append(w.buf, chunk...)
			}
		}
		if i < 0 {
			break
		}
		if err := w.emit(); err != nil {
			return 0, err
		}
		p = p[i+1:]
	}
	return n, nil
}

func (w *lineWriter) emit() error {
	err := w.line(w.buf, w.dropping)
	w.buf, w.dropping = w.buf[:0], false
	return err
}

func (w *lineWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	return w.emit()
}
