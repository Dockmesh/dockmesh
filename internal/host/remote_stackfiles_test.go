package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/stackfiles"
)

// deployAgent answers req.stack.deploy and hands each request it got to
// reqs, reporting the files it got as copied.
func deployAgent(t *testing.T, caps []string, reqs chan<- agents.StackDeployReq) *RemoteHost {
	t.Helper()
	ag, stop := agents.NewLoopback("a1", agents.HelloPayload{ProtocolVersion: agents.ProtocolVersion, Capabilities: caps},
		func(f agents.Frame, reply func(agents.Frame)) {
			if f.Type != agents.FrameReqStackDeploy {
				return
			}
			var req agents.StackDeployReq
			_ = json.Unmarshal(f.Payload, &req)
			reqs <- req
			res := compose.DeployResult{Stack: req.Name}
			if req.Files != nil {
				res.StackFiles = &stackfiles.Report{Copied: req.Files.Count()}
			}
			data, _ := json.Marshal(res)
			env, _ := json.Marshal(agents.ResponseEnvelope{OK: true, Data: data})
			reply(agents.Frame{Type: agents.FrameRes, ID: f.ID, Payload: env})
		})
	t.Cleanup(stop)
	return NewRemote("a1", "edge-1", &fakeLive{ag: ag})
}

func stackDirWith(t *testing.T, files map[string]string) StackFilesFunc {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return func(string) (*stackfiles.Set, []stackfiles.Note, error) { return stackfiles.Collect(dir) }
}

func TestRemoteDeployStackFiles(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{"compose.yaml": "services: {}", ".env": "S=1", "html/index.html": "hi"}

	t.Run("current agent gets the files", func(t *testing.T) {
		reqs := make(chan agents.StackDeployReq, 1)
		h := deployAgent(t, []string{agents.CapStackFiles}, reqs)
		h.stackFiles = stackDirWith(t, files)
		res, err := h.DeployStack(ctx, "web", "services: {}", "S=1")
		if err != nil {
			t.Fatal(err)
		}
		req := <-reqs
		if req.Files == nil || req.Files.Count() != 1 || req.Files.Files[len(req.Files.Files)-1].Path != "html/index.html" {
			t.Fatalf("files sent = %+v", req.Files)
		}
		if res.StackFiles == nil || res.StackFiles.Copied != 1 || res.StackFiles.NotSent != 0 {
			t.Fatalf("report = %+v", res.StackFiles)
		}
	})

	t.Run("older agent gets what it got before and the result says so", func(t *testing.T) {
		reqs := make(chan agents.StackDeployReq, 1)
		h := deployAgent(t, nil, reqs)
		h.stackFiles = stackDirWith(t, files)
		res, err := h.DeployStack(ctx, "web", "services: {}", "S=1")
		if err != nil {
			t.Fatal(err)
		}
		if req := <-reqs; req.Files != nil {
			t.Fatalf("older agent got files: %+v", req.Files)
		}
		if res.StackFiles == nil || res.StackFiles.NotSent != 1 {
			t.Fatalf("report = %+v", res.StackFiles)
		}
	})

	t.Run("older agent, nothing besides compose and env: no report", func(t *testing.T) {
		reqs := make(chan agents.StackDeployReq, 1)
		h := deployAgent(t, nil, reqs)
		h.stackFiles = stackDirWith(t, map[string]string{"compose.yaml": "services: {}"})
		res, err := h.DeployStack(ctx, "web", "services: {}", "")
		if err != nil {
			t.Fatal(err)
		}
		<-reqs
		if res.StackFiles != nil {
			t.Fatalf("report = %+v", res.StackFiles)
		}
	})

	t.Run("too large is refused before the agent is asked", func(t *testing.T) {
		reqs := make(chan agents.StackDeployReq, 1)
		h := deployAgent(t, []string{agents.CapStackFiles}, reqs)
		h.stackFiles = stackDirWith(t, map[string]string{"big.bin": strings.Repeat("x", stackfiles.MaxTotalBytes+1)})
		_, err := h.DeployStack(ctx, "web", "services: {}", "")
		if err == nil || !strings.Contains(err.Error(), "big.bin") {
			t.Fatalf("err = %v", err)
		}
		select {
		case req := <-reqs:
			t.Fatalf("agent asked: %+v", req.Name)
		default:
		}
	})
}
