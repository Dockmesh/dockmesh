package host

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

type fakeNode struct {
	mode fs.FileMode
	data []byte
	link string // raw symlink target, absolute or relative
}

// fakeContainerFS models the daemon's archive API on an in-memory tree:
// a path's directory is resolved in the scope of the root, its last
// element is not followed, and a link's LinkTarget is its fully resolved
// path, as the daemon reports it.
type fakeContainerFS struct {
	client.ContainerAPIClient // unused methods panic

	mu      sync.Mutex
	nodes   map[string]*fakeNode
	noRm    bool
	execs   [][]string
	execCmd []string
}

func newFakeFS() *fakeContainerFS {
	f := &fakeContainerFS{nodes: map[string]*fakeNode{"/": {mode: fs.ModeDir | 0o755}}}
	for _, d := range []string{"/etc", "/app", "/proc", "/proc/1", "/sys", "/dev", "/data"} {
		f.dir(d)
	}
	f.file("/etc/hostname", "web-1\n", 0o644)
	f.file("/etc/app.conf", "port=80\n", 0o600)
	f.file("/proc/1/environ", "SECRET=1", 0o400)
	f.file("/sys/kernel", "x", 0o444)
	f.nodes["/dev/null"] = &fakeNode{mode: fs.ModeDevice | fs.ModeCharDevice | 0o666}
	return f
}

func (f *fakeContainerFS) dir(p string) { f.nodes[p] = &fakeNode{mode: fs.ModeDir | 0o755} }
func (f *fakeContainerFS) link(p, target string) {
	f.nodes[p] = &fakeNode{mode: fs.ModeSymlink | 0o777, link: target}
}
func (f *fakeContainerFS) file(p, content string, perm fs.FileMode) {
	f.nodes[p] = &fakeNode{mode: perm, data: []byte(content)}
}

var errFakeNotFound = cerrdefs.ErrNotFound.WithMessage("Could not find the file in container")

// resolve follows every link in p within the root; followLast decides
// whether the last element is followed too.
func (f *fakeContainerFS) resolve(p string, followLast bool, depth int) (string, error) {
	if depth > 40 {
		return "", errors.New("too many links")
	}
	cur := "/"
	parts := strings.Split(strings.Trim(path.Clean(p), "/"), "/")
	for i, comp := range parts {
		if comp == "" {
			continue
		}
		next := path.Join(cur, comp)
		n, ok := f.nodes[next]
		if ok && n.mode&fs.ModeSymlink != 0 && (followLast || i < len(parts)-1) {
			t := n.link
			if !strings.HasPrefix(t, "/") {
				t = path.Join(cur, t)
			}
			r, err := f.resolve(t, true, depth+1)
			if err != nil {
				return "", err
			}
			next = r
		}
		cur = next
	}
	return cur, nil
}

func (f *fakeContainerFS) ContainerStatPath(_ context.Context, _, p string) (container.PathStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolve(p, false, 0)
	if err != nil {
		return container.PathStat{}, err
	}
	n, ok := f.nodes[r]
	if !ok {
		return container.PathStat{}, errFakeNotFound
	}
	st := container.PathStat{Name: path.Base(r), Size: int64(len(n.data)), Mode: n.mode}
	if n.mode&fs.ModeSymlink != 0 {
		if st.LinkTarget, err = f.resolve(r, true, 0); err != nil {
			return container.PathStat{}, err
		}
	}
	return st, nil
}

func (f *fakeContainerFS) CopyFromContainer(_ context.Context, _, p string) (io.ReadCloser, container.PathStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.resolve(p, false, 0)
	if err != nil {
		return nil, container.PathStat{}, err
	}
	root, ok := f.nodes[r]
	if !ok {
		return nil, container.PathStat{}, errFakeNotFound
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(name string, n *fakeNode) {
		h := &tar.Header{Name: name, Mode: int64(n.mode.Perm()), Size: int64(len(n.data)), ModTime: time.Unix(0, 0)}
		switch {
		case n.mode.IsDir():
			h.Typeflag, h.Size, h.Name = tar.TypeDir, 0, name+"/"
		case n.mode&fs.ModeSymlink != 0:
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, n.link
		case n.mode.IsRegular():
			h.Typeflag = tar.TypeReg
		default:
			h.Typeflag, h.Size = tar.TypeChar, 0
		}
		_ = tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(n.data)
		}
	}
	prefix := path.Base(r)
	if r == "/" {
		prefix = ""
	}
	write(prefix, root)
	if root.mode.IsDir() {
		var names []string
		for k := range f.nodes {
			if k != r && strings.HasPrefix(k, strings.TrimSuffix(r, "/")+"/") {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			rel := strings.TrimPrefix(k, strings.TrimSuffix(r, "/")+"/")
			name := prefix + "/" + rel
			if r == "/" {
				name = "/" + rel
			}
			write(name, f.nodes[k])
		}
	}
	_ = tw.Close()
	return io.NopCloser(&buf), container.PathStat{}, nil
}

func (f *fakeContainerFS) CopyToContainer(_ context.Context, _, dst string, content io.Reader, _ container.CopyToContainerOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir, err := f.resolve(dst, true, 0)
	if err != nil {
		return err
	}
	tr := tar.NewReader(content)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(tr)
		f.nodes[path.Join(dir, h.Name)] = &fakeNode{mode: fs.FileMode(h.Mode).Perm(), data: data}
	}
}

func (f *fakeContainerFS) ContainerExecCreate(_ context.Context, _ string, opts container.ExecOptions) (container.ExecCreateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noRm {
		return container.ExecCreateResponse{}, errors.New("container is not running")
	}
	f.execs = append(f.execs, opts.Cmd)
	f.execCmd = opts.Cmd
	return container.ExecCreateResponse{ID: "exec1"}, nil
}

func (f *fakeContainerFS) ContainerExecAttach(context.Context, string, container.ExecStartOptions) (types.HijackedResponse, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return types.HijackedResponse{Conn: c1, Reader: bufio.NewReader(strings.NewReader(""))}, nil
}

func (f *fakeContainerFS) ContainerExecInspect(context.Context, string) (container.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.execCmd) == 3 && f.execCmd[0] == "rm" && f.execCmd[1] == "--" {
		delete(f.nodes, f.execCmd[2])
	}
	return container.ExecInspect{ExitCode: 0}, nil
}

func TestContainerPathRules(t *testing.T) {
	f := newFakeFS()
	f.dir("/app/conf")
	f.file("/app/conf/a.yml", "a: 1\n", 0o644)
	f.link("/app/up", "../../../../etc")
	f.link("/app/proc", "/proc/1")
	f.link("/app/envlink", "/proc/1/environ")
	f.link("/app/sys", "../sys")
	f.link("/app/abs", "/etc/hostname")
	f.link("/app/loop", "/app/loop")
	f.link("/app/hop", "/app/up")
	f.nodes["/app/tty"] = &fakeNode{mode: fs.ModeDevice | 0o600}
	f.nodes["/app/fifo"] = &fakeNode{mode: fs.ModeNamedPipe | 0o600}
	ctx := context.Background()

	browse := []struct {
		path    string
		want    []string
		wantErr error
	}{
		{path: "/app/conf", want: []string{"a.yml"}},
		{path: "/app/conf/", want: []string{"a.yml"}},
		{path: "/app/../app/./conf", want: []string{"a.yml"}},
		{path: "/../../../app/conf", want: []string{"a.yml"}},
		{path: "/app/up", want: []string{"app.conf", "hostname"}},
		{path: "/app/hop", want: []string{"app.conf", "hostname"}},
		{path: "app/conf", wantErr: ErrContainerPathInvalid},
		{path: "", wantErr: ErrContainerPathInvalid},
		{path: "/app\x00/conf", wantErr: ErrContainerPathInvalid},
		{path: "/" + strings.Repeat("a/", 100), wantErr: ErrVolumePathTooLong},
		{path: "/" + strings.Repeat("a", MaxBrowsePathLen), wantErr: ErrVolumePathTooLong},
		{path: "/proc", wantErr: ErrContainerPathDenied},
		{path: "/proc/1", wantErr: ErrContainerPathDenied},
		{path: "/sys", wantErr: ErrContainerPathDenied},
		{path: "/dev", wantErr: ErrContainerPathDenied},
		{path: "/app/proc", wantErr: ErrContainerPathDenied},
		{path: "/app/sys", wantErr: ErrContainerPathDenied},
		{path: "/app/../proc", wantErr: ErrContainerPathDenied},
		{path: "/etc/hostname", wantErr: ErrContainerNotDir},
		{path: "/missing", wantErr: fs.ErrNotExist},
		{path: "/app/loop", wantErr: errors.New("too many links")},
	}
	for _, tt := range browse {
		got, err := BrowseContainerDir(ctx, f, "c1", tt.path)
		if tt.wantErr != nil {
			if err == nil || (!errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error())) {
				t.Errorf("browse %q: err %v, want %v", tt.path, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("browse %q: %v", tt.path, err)
			continue
		}
		var names []string
		for _, e := range got {
			names = append(names, e.Name)
		}
		sort.Strings(names)
		if strings.Join(names, ",") != strings.Join(tt.want, ",") {
			t.Errorf("browse %q: %v, want %v", tt.path, names, tt.want)
		}
	}

	reads := []struct {
		path    string
		want    string
		wantErr error
	}{
		{path: "/etc/hostname", want: "web-1\n"},
		{path: "/app/abs", want: "web-1\n"},
		{path: "/app/up/hostname", want: "web-1\n"},
		{path: "/app/../../etc/hostname", want: "web-1\n"},
		{path: "/proc/1/environ", wantErr: ErrContainerPathDenied},
		{path: "/app/envlink", wantErr: ErrContainerPathDenied},
		{path: "/app/proc/environ", wantErr: ErrContainerPathDenied},
		{path: "/sys/kernel", wantErr: ErrContainerPathDenied},
		{path: "/dev/null", wantErr: ErrContainerPathDenied},
		{path: "/app/tty", wantErr: ErrContainerPathDenied},
		{path: "/app/fifo", wantErr: ErrContainerPathDenied},
		{path: "/etc", wantErr: ErrContainerNotFile},
		{path: "/etc/missing", wantErr: fs.ErrNotExist},
	}
	for _, tt := range reads {
		got, err := ReadContainerFile(ctx, f, "c1", tt.path, 0)
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("read %q: err %v, want %v", tt.path, err, tt.wantErr)
			}
			if _, _, err := DownloadContainerFile(ctx, f, "c1", tt.path); !errors.Is(err, tt.wantErr) {
				t.Errorf("download %q: err %v, want %v", tt.path, err, tt.wantErr)
			}
			continue
		}
		if err != nil || string(got.Content) != tt.want {
			t.Errorf("read %q: %+v, %v; want %q", tt.path, got, err, tt.want)
		}
	}
}

func TestContainerWriteAndDelete(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name    string
		path    string
		mode    int64
		wantErr error
		check   func(t *testing.T, f *fakeContainerFS)
	}{
		{name: "new file gets 0644", path: "/app/new.txt", check: func(t *testing.T, f *fakeContainerFS) {
			if n := f.nodes["/app/new.txt"]; n == nil || n.mode.Perm() != 0o644 || string(n.data) != "hello" {
				t.Errorf("new file: %+v", n)
			}
		}},
		{name: "edit keeps mode", path: "/etc/app.conf", check: func(t *testing.T, f *fakeContainerFS) {
			if n := f.nodes["/etc/app.conf"]; n.mode.Perm() != 0o600 || string(n.data) != "hello" {
				t.Errorf("edited file: %v %q", n.mode, n.data)
			}
		}},
		{name: "explicit mode", path: "/app/run.sh", mode: 0o755, check: func(t *testing.T, f *fakeContainerFS) {
			if n := f.nodes["/app/run.sh"]; n.mode.Perm() != 0o755 {
				t.Errorf("mode %v", n.mode)
			}
		}},
		{name: "parent through link", path: "/app/up/new.conf", check: func(t *testing.T, f *fakeContainerFS) {
			if f.nodes["/etc/new.conf"] == nil {
				t.Error("file not written into the link's target directory")
			}
		}},
		{name: "through a link", path: "/app/abs", wantErr: ErrContainerSymlink},
		{name: "into proc", path: "/proc/1/environ", wantErr: ErrContainerPathDenied},
		{name: "into proc via link", path: "/app/proc/x", wantErr: ErrContainerPathDenied},
		{name: "onto a device", path: "/dev/null", wantErr: ErrContainerPathDenied},
		{name: "onto a directory", path: "/app", wantErr: ErrContainerNotFile},
		{name: "root", path: "/", wantErr: ErrContainerNotFile},
		{name: "missing parent", path: "/nope/x", wantErr: fs.ErrNotExist},
		{name: "relative", path: "app/x", wantErr: ErrContainerPathInvalid},
		{name: "bad mode", path: "/app/x", mode: 0o10000, wantErr: errors.New("mode must be between")},
	}
	for _, tt := range writes {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeFS()
			f.link("/app/up", "../../etc")
			f.link("/app/abs", "/etc/hostname")
			f.link("/app/proc", "/proc/1")
			err := WriteContainerFile(ctx, f, "c1", tt.path, []byte("hello"), tt.mode)
			if tt.wantErr != nil {
				if err == nil || (!errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error())) {
					t.Fatalf("err %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, f)
		})
	}

	t.Run("size limit", func(t *testing.T) {
		f := newFakeFS()
		if err := WriteContainerFile(ctx, f, "c1", "/app/big", make([]byte, MaxContainerUploadBytes+1), 0); !errors.Is(err, ErrContainerFileTooLarge) {
			t.Fatalf("over the limit: %v", err)
		}
		if err := WriteContainerFile(ctx, f, "c1", "/app/big", make([]byte, MaxContainerUploadBytes), 0); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		f := newFakeFS()
		f.link("/app/abs", "/etc/hostname")
		if err := DeleteContainerFile(ctx, f, "c1", "/app/abs"); err != nil {
			t.Fatal(err)
		}
		if f.nodes["/app/abs"] != nil || f.nodes["/etc/hostname"] == nil {
			t.Fatal("delete of a link must remove the link, not its target")
		}
		if err := DeleteContainerFile(ctx, f, "c1", "/etc/hostname"); err != nil || f.nodes["/etc/hostname"] != nil {
			t.Fatalf("delete file: %v", err)
		}
		for p, want := range map[string]error{
			"/etc":            ErrContainerNotFile,
			"/etc/missing":    fs.ErrNotExist,
			"/proc/1/environ": ErrContainerPathDenied,
			"/dev/null":       ErrContainerPathDenied,
		} {
			if err := DeleteContainerFile(ctx, f, "c1", p); !errors.Is(err, want) {
				t.Errorf("delete %s: %v, want %v", p, err, want)
			}
		}
		for _, cmd := range f.execs {
			if len(cmd) != 3 || cmd[0] != "rm" || cmd[1] != "--" {
				t.Errorf("unexpected exec %q", cmd)
			}
		}
		f.noRm = true
		if err := DeleteContainerFile(ctx, f, "c1", "/etc/app.conf"); !errors.Is(err, ErrContainerNoRm) {
			t.Fatalf("stopped container: %v", err)
		}
	})
}

func TestContainerReadLimits(t *testing.T) {
	ctx := context.Background()
	f := newFakeFS()
	f.file("/data/big.log", strings.Repeat("x", 2*MaxContainerPreviewBytes), 0o644)
	for _, maxBytes := range []int64{0, 5 * MaxContainerPreviewBytes} {
		res, err := ReadContainerFile(ctx, f, "c1", "/data/big.log", maxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Truncated || len(res.Content) != MaxContainerPreviewBytes || res.Size != 2*MaxContainerPreviewBytes {
			t.Fatalf("max %d: truncated=%v len=%d size=%d", maxBytes, res.Truncated, len(res.Content), res.Size)
		}
	}
	body, hdr, err := DownloadContainerFile(ctx, f, "c1", "/data/big.log")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, body)
	body.Close()
	if n != 2*MaxContainerPreviewBytes || hdr.Size != n {
		t.Fatalf("download: %d bytes, header %d", n, hdr.Size)
	}

	for i := 0; i <= maxContainerDirEntries; i++ {
		f.file("/data/many/"+strconv.Itoa(i), "", 0o644)
	}
	f.dir("/data/many")
	if _, err := BrowseContainerDir(ctx, f, "c1", "/data/many"); !errors.Is(err, ErrContainerDirTooLarge) {
		t.Fatalf("large directory: %v", err)
	}
}

// fakeAgentSession serves container file frames the way the agent does,
// against f.
func fakeAgentSession(t *testing.T, f *fakeContainerFS, caps ...string) *RemoteHost {
	t.Helper()
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: caps},
		func(fr agents.Frame, reply func(agents.Frame)) {
			ctx := context.Background()
			if fr.Type == agents.FrameStreamOpen {
				var open agents.StreamOpen
				_ = json.Unmarshal(fr.Payload, &open)
				p, _ := open.Params["path"].(string)
				msg := ""
				body, _, err := DownloadContainerFile(ctx, f, open.Container, p)
				if err == nil {
					buf := make([]byte, 32<<10)
					for {
						n, rerr := body.Read(buf)
						if n > 0 {
							d, _ := json.Marshal(agents.StreamData{StreamID: open.StreamID, Data: append([]byte(nil), buf[:n]...)})
							reply(agents.Frame{Type: agents.FrameStreamData, Payload: d})
						}
						if rerr != nil {
							break
						}
					}
					body.Close()
				} else {
					msg = err.Error()
				}
				c, _ := json.Marshal(agents.StreamClose{StreamID: open.StreamID, Error: msg})
				reply(agents.Frame{Type: agents.FrameStreamClose, Payload: c})
				return
			}
			res, err := ServeContainerFileRequest(ctx, f, fr)
			env := agents.ResponseEnvelope{OK: err == nil}
			if err != nil {
				env.Error = err.Error()
			} else {
				env.Data, _ = json.Marshal(res)
			}
			payload, _ := json.Marshal(env)
			reply(agents.Frame{Type: agents.FrameRes, ID: fr.ID, Payload: payload})
		})
	t.Cleanup(stop)
	return NewRemote("a1", "edge-1", &fakeLive{ag: ag})
}

func TestRemoteContainerFilesRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newFakeFS()
	f.file("/data/blob.bin", "\x00\x01binary", 0o644)
	f.file("/data/big.log", strings.Repeat("y", 300<<10), 0o644)
	f.link("/data/env", "/proc/1/environ")
	h := fakeAgentSession(t, f, agents.CapContainerFiles)

	entries, err := h.ContainerBrowseEntries(ctx, "c1", "/etc")
	if err != nil || len(entries) != 2 {
		t.Fatalf("browse: %v %v", entries, err)
	}
	res, err := h.ContainerReadFile(ctx, "c1", "/data/blob.bin", 0)
	if err != nil || string(res.Content) != "\x00\x01binary" || !res.Binary {
		t.Fatalf("read: %+v %v", res, err)
	}
	rc, name, size, err := h.ContainerDownloadFile(ctx, "c1", "/data/big.log")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || name != "big.log" || size != 300<<10 || len(got) != 300<<10 {
		t.Fatalf("download: name %q size %d got %d err %v", name, size, len(got), err)
	}
	if err := h.ContainerWriteFile(ctx, "c1", "/data/new.txt", []byte("written"), 0); err != nil {
		t.Fatal(err)
	}
	if string(f.nodes["/data/new.txt"].data) != "written" {
		t.Fatal("write did not reach the agent's container")
	}
	if err := h.ContainerDeleteFile(ctx, "c1", "/data/new.txt"); err != nil || f.nodes["/data/new.txt"] != nil {
		t.Fatalf("delete: %v", err)
	}

	// The agent's sentinel errors survive the wire, so handlers map them
	// to the same status codes as on the server host.
	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"denied", func() error { _, err := h.ContainerReadFile(ctx, "c1", "/data/env", 0); return err }(), ErrContainerPathDenied},
		{"not found", func() error { _, err := h.ContainerBrowseEntries(ctx, "c1", "/nope"); return err }(), fs.ErrNotExist},
		{"not a file", func() error { _, _, _, err := h.ContainerDownloadFile(ctx, "c1", "/etc"); return err }(), ErrContainerNotFile},
		{"symlink write", h.ContainerWriteFile(ctx, "c1", "/data/env", []byte("x"), 0), ErrContainerSymlink},
		{"too large", h.ContainerWriteFile(ctx, "c1", "/data/x", make([]byte, MaxContainerUploadBytes+1), 0), ErrContainerFileTooLarge},
	} {
		if !errors.Is(tt.err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, tt.err, tt.want)
		}
	}

	// The largest allowed upload fits in one frame.
	frame, _ := json.Marshal(agents.ContainerFileReq{Container: "c1", Path: "/data/x", Data: make([]byte, MaxContainerUploadBytes)})
	if len(frame) >= agents.MaxFrameBytes-(64<<10) {
		t.Fatalf("largest upload frame is %d bytes, limit %d", len(frame), agents.MaxFrameBytes)
	}
	if base64.StdEncoding.EncodedLen(MaxContainerUploadBytes) >= agents.MaxFrameBytes {
		t.Fatal("upload limit exceeds the frame size")
	}
}

func TestOldAgentGetsUpdateHint(t *testing.T) {
	ctx := context.Background()
	h := fakeAgentSession(t, newFakeFS(), agents.CapStreamCredit)
	checks := map[string]error{}
	_, checks["browse"] = h.ContainerBrowseEntries(ctx, "c1", "/")
	_, checks["read"] = h.ContainerReadFile(ctx, "c1", "/etc/hostname", 0)
	_, _, _, checks["download"] = h.ContainerDownloadFile(ctx, "c1", "/etc/hostname")
	checks["write"] = h.ContainerWriteFile(ctx, "c1", "/etc/x", []byte("x"), 0)
	checks["delete"] = h.ContainerDeleteFile(ctx, "c1", "/etc/hostname")
	for op, err := range checks {
		if !errors.Is(err, ErrAgentNoContainerFiles) || !strings.Contains(err.Error(), "update it") {
			t.Errorf("%s on an old agent: %v", op, err)
		}
	}
}
