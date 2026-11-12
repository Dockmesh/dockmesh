package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/image"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scanner"
)

// imageHost records image calls; pullBody and pullErr shape a pull.
type imageHost struct {
	recordingHost
	pullBody string
	pullErr  error
}

func (f *imageHost) InspectImage(_ context.Context, id string) (image.InspectResponse, error) {
	if id == "absent" {
		return image.InspectResponse{}, errors.New("No such image: absent")
	}
	return image.InspectResponse{ID: "sha256:" + f.id, RepoTags: []string{id}}, nil
}

func (f *imageHost) ImageHistory(_ context.Context, id string) ([]image.HistoryResponseItem, error) {
	f.record("image.history " + id)
	switch id {
	case "absent":
		return nil, errors.New("No such image: absent")
	case "old-agent":
		return nil, host.ErrAgentNoImageHistory
	}
	return []image.HistoryResponseItem{
		{ID: "sha256:top", Created: 1700000100, CreatedBy: "CMD nginx", Size: 12, Comment: "top", Tags: []string{"nginx:1"}},
		{ID: "<missing>", Created: 1700000000, CreatedBy: "ADD file", Size: 34},
	}, nil
}

func (f *imageHost) PullImage(_ context.Context, ref, _ string) (io.ReadCloser, error) {
	f.record("image.pull " + ref)
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	return io.NopCloser(strings.NewReader(f.pullBody)), nil
}

// agentImageHost is an imageHost the scanner can only reach by export.
type agentImageHost struct{ imageHost }

func (f *agentImageHost) ExportImage(_ context.Context, ref string) (io.ReadCloser, error) {
	f.record("image.export " + ref)
	return io.NopCloser(strings.NewReader("archive of " + f.id)), nil
}

// fakeScanner reports one critical finding on the agent's archive only, so
// a report that crosses hosts is visible.
type fakeScanner struct{ calls []string }

func (s *fakeScanner) Name() string { return "fake" }
func (s *fakeScanner) Ready() error { return nil }

func (s *fakeScanner) Scan(_ context.Context, ref string) (*scanner.Report, error) {
	s.calls = append(s.calls, "daemon "+ref)
	return &scanner.Report{Image: ref, Scanner: "fake", Vulnerabilities: []scanner.Vulnerability{}}, nil
}

func (s *fakeScanner) ScanArchive(_ context.Context, archive io.Reader, ref string) (*scanner.Report, error) {
	b, err := io.ReadAll(archive)
	if err != nil {
		return nil, err
	}
	s.calls = append(s.calls, fmt.Sprintf("archive %s (%s)", ref, b))
	rep := &scanner.Report{Image: ref, Scanner: "fake", Vulnerabilities: []scanner.Vulnerability{}}
	rep.Summary.Critical = 1
	return rep, nil
}

type imageHostEnv struct {
	*authzEnv
	local      *imageHost
	agent      *agentImageHost
	scan       *fakeScanner
	boss, edge string
}

func newImageHostEnv(t *testing.T) *imageHostEnv {
	t.Helper()
	e := newScopeEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'online')`); err != nil {
		t.Fatal(err)
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-1", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags

	env := &imageHostEnv{
		authzEnv: e,
		local:    &imageHost{recordingHost: recordingHost{id: "local"}, pullBody: "{\"status\":\"local\"}\n"},
		agent:    &agentImageHost{imageHost{recordingHost: recordingHost{id: "agent-1"}, pullBody: "{\"status\":\"agent\"}\n"}},
		scan:     &fakeScanner{},
	}
	e.h.Hosts = host.NewFixedRegistry(env.local, env.agent)
	e.h.Scanner = env.scan
	e.h.ScanStore = scanner.NewStore(e.db)

	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermImagesCreate)).Post("/images/pull", e.h.PullImage)
		r.With(middleware.RequirePerm(rbac.PermImagesView)).Get("/images/{id}/history", e.h.ImageHistory)
		r.With(middleware.RequirePerm(rbac.PermImagesScan)).Post("/images/{id}/scan", e.h.ScanImage)
		r.With(middleware.RequirePerm(rbac.PermImagesScan)).Get("/images/{id}/scan", e.h.GetScan)
	})
	e.user("boss", "admin")
	scoped := e.user("edge", "host-admin")
	e.scopeUser(scoped.ID, "prod")
	env.boss = e.login("boss").AccessToken
	env.edge = e.login("edge").AccessToken
	return env
}

func hostQuery(path, id string) string {
	if id == "" {
		return path
	}
	return path + "?host=" + id
}

// A pull and a scan for one host never touch the other one.
func TestImagePullAndScanStayOnTheirHost(t *testing.T) {
	targets := []struct {
		name, query, status, scanCall string
		reached, spared               func(*imageHostEnv) *recordingHost
		wantReached                   []string
	}{
		{
			name: "agent", query: "agent-1", status: "agent", scanCall: "archive nginx:1 (archive of agent-1)",
			reached:     func(e *imageHostEnv) *recordingHost { return &e.agent.recordingHost },
			spared:      func(e *imageHostEnv) *recordingHost { return &e.local.recordingHost },
			wantReached: []string{"image.pull nginx:1", "image.export nginx:1"},
		},
		{
			name: "server", query: "local", status: "local", scanCall: "daemon nginx:1",
			reached:     func(e *imageHostEnv) *recordingHost { return &e.local.recordingHost },
			spared:      func(e *imageHostEnv) *recordingHost { return &e.agent.recordingHost },
			wantReached: []string{"image.pull nginx:1"},
		},
		{
			name: "no host means the server", query: "", status: "local", scanCall: "daemon nginx:1",
			reached:     func(e *imageHostEnv) *recordingHost { return &e.local.recordingHost },
			spared:      func(e *imageHostEnv) *recordingHost { return &e.agent.recordingHost },
			wantReached: []string{"image.pull nginx:1"},
		},
	}
	for _, tg := range targets {
		t.Run(tg.name, func(t *testing.T) {
			e := newImageHostEnv(t)
			rr := e.do(http.MethodPost, hostQuery("/images/pull", tg.query), e.boss, map[string]string{"image": "nginx:1"})
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status":"`+tg.status+`"`) {
				t.Fatalf("pull: %d %s", rr.Code, rr.Body)
			}
			if rr := e.do(http.MethodPost, hostQuery("/images/nginx:1/scan", tg.query), e.boss, nil); rr.Code != http.StatusOK {
				t.Fatalf("scan: %d %s", rr.Code, rr.Body)
			}
			if !slices.Equal(e.scan.calls, []string{tg.scanCall}) {
				t.Fatalf("scanner calls %v, want [%s]", e.scan.calls, tg.scanCall)
			}
			if got := tg.reached(e).took(); !slices.Equal(got, tg.wantReached) {
				t.Fatalf("host got %v, want %v", got, tg.wantReached)
			}
			if got := tg.spared(e).took(); len(got) != 0 {
				t.Fatalf("other host was touched: %v", got)
			}
		})
	}
}

// A user scoped to the agent reaches the agent and nothing on the server.
func TestImageCallsFollowHostScope(t *testing.T) {
	e := newImageHostEnv(t)
	calls := []struct {
		name, method, path string
		body               any
	}{
		{"pull", http.MethodPost, "/images/pull", map[string]string{"image": "nginx:1"}},
		{"scan", http.MethodPost, "/images/nginx:1/scan", nil},
		{"cached scan", http.MethodGet, "/images/nginx:1/scan", nil},
		{"history", http.MethodGet, "/images/nginx:1/history", nil},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			if rr := e.do(c.method, hostQuery(c.path, "local"), e.edge, c.body); rr.Code != http.StatusForbidden {
				t.Fatalf("server host: %d, want 403: %s", rr.Code, rr.Body)
			}
			if rr := e.do(c.method, hostQuery(c.path, "agent-1"), e.edge, c.body); rr.Code != http.StatusOK {
				t.Fatalf("own host: %d %s", rr.Code, rr.Body)
			}
			if rr := e.do(c.method, hostQuery(c.path, "nope"), e.boss, c.body); rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("unknown host: %d, want 503: %s", rr.Code, rr.Body)
			}
		})
	}
	if got := e.local.took(); len(got) != 0 {
		t.Fatalf("server host was touched by an agent-scoped caller: %v", got)
	}
}

// The same tag on two hosts has one report each.
func TestScanReportsAreKeptPerHost(t *testing.T) {
	e := newImageHostEnv(t)
	if rr := e.do(http.MethodPost, hostQuery("/images/nginx:1/scan", "agent-1"), e.boss, nil); rr.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", rr.Code, rr.Body)
	}
	var onAgent struct {
		Image   string `json:"image"`
		Summary struct {
			Critical int `json:"critical"`
		} `json:"summary"`
	}
	rr := e.do(http.MethodGet, hostQuery("/images/nginx:1/scan", "agent-1"), e.boss, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &onAgent); err != nil {
		t.Fatalf("%v: %s", err, rr.Body)
	}
	if onAgent.Image != "nginx:1" || onAgent.Summary.Critical != 1 {
		t.Fatalf("agent report = %+v", onAgent)
	}
	rr = e.do(http.MethodGet, "/images/nginx:1/scan", e.boss, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"scanned":false`) {
		t.Fatalf("server host sees the agent's report: %d %s", rr.Code, rr.Body)
	}
}

func TestImageFailuresReachTheCaller(t *testing.T) {
	t.Run("older agent gets the update message", func(t *testing.T) {
		e := newImageHostEnv(t)
		e.agent.pullErr = host.ErrAgentNoImageTransfer
		rr := e.do(http.MethodPost, hostQuery("/images/pull", "agent-1"), e.boss, map[string]string{"image": "nginx:1"})
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "update it from the Hosts page") {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
	})
	t.Run("a pull the daemon refuses keeps its status", func(t *testing.T) {
		e := newImageHostEnv(t)
		e.local.pullErr = errors.New("pull access denied for private/app")
		rr := e.do(http.MethodPost, "/images/pull", e.boss, map[string]string{"image": "private/app"})
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
	})
	t.Run("an in-stream failure is relayed", func(t *testing.T) {
		e := newImageHostEnv(t)
		e.agent.pullBody = "{\"status\":\"Pulling\"}\n{\"error\":\"toomanyrequests: rate limit\"}\n"
		rr := e.do(http.MethodPost, hostQuery("/images/pull", "agent-1"), e.boss, map[string]string{"image": "nginx:1"})
		if !strings.Contains(rr.Body.String(), "toomanyrequests") {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
	})
	t.Run("an image missing on the host is not scanned elsewhere", func(t *testing.T) {
		e := newImageHostEnv(t)
		rr := e.do(http.MethodPost, hostQuery("/images/absent/scan", "agent-1"), e.boss, nil)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
		if len(e.scan.calls) != 0 {
			t.Fatalf("scanner ran: %v", e.scan.calls)
		}
	})
}

// A stream that breaks off after the 200 must still read as a failure.
func TestBrokenPullStreamEndsWithAnErrorLine(t *testing.T) {
	e := newImageHostEnv(t)
	e.h.Hosts = host.NewFixedRegistry(e.local, &brokenPullHost{agentImageHost: e.agent})
	rr := e.do(http.MethodPost, hostQuery("/images/pull", "agent-1"), e.boss, map[string]string{"image": "nginx:1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if !strings.Contains(lines[len(lines)-1], `"error":"agent went away"`) {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
}

type brokenPullHost struct{ *agentImageHost }

func (f *brokenPullHost) PullImage(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(io.MultiReader(strings.NewReader("{\"status\":\"Pulling\"}\n"), failingReader{})), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("agent went away") }

func TestImageHistoryEndpoint(t *testing.T) {
	e := newImageHostEnv(t)
	rr := e.do(http.MethodGet, hostQuery("/images/nginx:1/history", "agent-1"), e.boss, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rr.Code, rr.Body)
	}
	var got []imageHistoryEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CreatedBy != "CMD nginx" || got[0].ID != "sha256:top" || got[0].Size != 12 || got[1].ID != "" {
		t.Fatalf("history = %+v", got)
	}
	if !slices.Contains(e.agent.took(), "image.history nginx:1") {
		t.Fatalf("agent not asked: %v", e.agent.took())
	}
	for id, want := range map[string]int{"absent": http.StatusUnprocessableEntity, "old-agent": http.StatusServiceUnavailable} {
		if rr := e.do(http.MethodGet, hostQuery("/images/"+id+"/history", "agent-1"), e.boss, nil); rr.Code != want {
			t.Errorf("%s: %d, want %d: %s", id, rr.Code, want, rr.Body)
		}
	}
}
