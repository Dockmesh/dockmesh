package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/distribution/reference"
	"github.com/docker/docker/client"

	"github.com/dockmesh/dockmesh/internal/operations"
)

func TestReadPullStream(t *testing.T) {
	cases := []struct {
		name    string
		stream  string
		wantErr string
	}{
		{"ok", `{"status":"Pulling from library/web","id":"latest"}` + "\n" + `{"status":"Digest: sha256:abc"}` + "\n", ""},
		{"rate limit", `{"status":"Pulling from library/web","id":"latest"}` + "\n" + `{"errorDetail":{"message":"toomanyrequests"},"error":"toomanyrequests: rate limit"}` + "\n" + `{"error":"second"}` + "\n", "toomanyrequests: rate limit"},
		{"masked", `{"error":"denied for c2VjcmV0"}` + "\n", "denied for " + operations.Masked},
		{"garbage after error", `{"error":"boom"}` + "\nnot json", "boom"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ReadPullStream(strings.NewReader(tc.stream), []string{"c2VjcmV0"})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPullAnonymous(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"pulled", http.StatusOK, `{"status":"Pull complete"}` + "\n", ""},
		{"failure inside the stream", http.StatusOK, `{"status":"Pulling"}` + "\n" + `{"error":"toomanyrequests: rate limit"}` + "\n", "toomanyrequests: rate limit"},
		{"refused by the daemon", http.StatusNotFound, `{"message":"manifest unknown"}`, "manifest unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/images/create") {
					t.Errorf("unexpected call %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithVersion("1.45"), client.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			err = PullAnonymous(context.Background(), cli, "busybox:1")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestBusyboxHelperPinned(t *testing.T) {
	ref, err := reference.ParseNormalizedNamed(BusyboxHelperImage)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ref.(reference.Digested); !ok {
		t.Fatalf("%s is not pinned by digest", BusyboxHelperImage)
	}
}
