package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// fakeDocker records the container API calls a swap makes and fails
// the step named in failOn.
type fakeDocker struct {
	mu     sync.Mutex
	calls  []string
	failOn string
}

var apiVersion = regexp.MustCompile(`^/v[0-9.]+`)

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := apiVersion.ReplaceAllString(r.URL.Path, "")
	call := r.Method + " " + path
	if n := r.URL.Query().Get("name"); n != "" {
		call += "?name=" + n
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	fail := f.failOn != "" && strings.HasPrefix(call, f.failOn)
	f.mu.Unlock()
	if fail {
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(call, "POST /containers/create"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"new"}`))
	case call == "GET /containers/new/json":
		_, _ = w.Write([]byte(`{"Id":"new","State":{"Running":true,"ExitCode":0}}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeDocker) client(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.43"))
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

func oldContainer() *container.InspectResponse {
	return &container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         "old",
			Name:       "/web",
			State:      &container.State{Running: true},
			HostConfig: &container.HostConfig{},
		},
		Config: &container.Config{Image: "nginx:1.27"},
	}
}

func TestSwapContainerRestoresOldOnFailure(t *testing.T) {
	tests := []struct {
		failOn string
		want   []string
	}{
		{
			failOn: "POST /containers/create",
			want: []string{
				"POST /containers/old/rename?name=web-dmold-",
				"POST /containers/old/stop",
				"POST /containers/create?name=web",
				"POST /containers/old/rename?name=web",
				"POST /containers/old/start",
			},
		},
		{
			failOn: "POST /containers/new/start",
			want: []string{
				"POST /containers/old/rename?name=web-dmold-",
				"POST /containers/old/stop",
				"POST /containers/create?name=web",
				"POST /containers/new/start",
				"DELETE /containers/new",
				"POST /containers/old/rename?name=web",
				"POST /containers/old/start",
			},
		},
	}
	for _, tt := range tests {
		f := &fakeDocker{failOn: tt.failOn}
		cli := f.client(t)
		old := oldContainer()
		_, err := swapContainer(context.Background(), cli, old, standaloneSpec(old, "nginx:1.28", nil))
		if err == nil {
			t.Fatalf("%s: swap succeeded", tt.failOn)
		}
		assertCalls(t, f.calls, tt.want)
	}
}

func TestSwapContainerRemovesOldAfterSuccess(t *testing.T) {
	f := &fakeDocker{}
	cli := f.client(t)
	old := oldContainer()
	id, err := swapContainer(context.Background(), cli, old, standaloneSpec(old, "nginx:1.28", nil))
	if err != nil {
		t.Fatal(err)
	}
	if id != "new" {
		t.Fatalf("id = %q", id)
	}
	last := f.calls[len(f.calls)-1]
	if last != "DELETE /containers/old" {
		t.Fatalf("last call = %q, want old container removed", last)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "DELETE /containers/old") && c != last {
			t.Fatalf("old container removed before the new one ran: %v", f.calls)
		}
	}
}

func TestSwapContainerRefusesAutoRemove(t *testing.T) {
	f := &fakeDocker{}
	cli := f.client(t)
	old := oldContainer()
	old.HostConfig.AutoRemove = true
	if _, err := swapContainer(context.Background(), cli, old, standaloneSpec(old, "nginx:1.28", nil)); err == nil {
		t.Fatal("swap of an auto-remove container succeeded")
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls made: %v", f.calls)
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls:\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("call %d = %q, want prefix %q\nall: %v", i, got[i], want[i], got)
		}
	}
}
