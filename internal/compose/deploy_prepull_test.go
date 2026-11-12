package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/client"
)

// A pull that fails inside the stream (HTTP 200) fails the deploy when no
// local copy exists, and falls back to the cached image when one does.
func TestPrePullStreamError(t *testing.T) {
	cases := []struct {
		name    string
		local   bool
		wantErr bool
	}{
		{"missing locally", false, true},
		{"cached copy", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/json") && strings.Contains(r.URL.Path, "/images/"):
					if !tc.local {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"message":"No such image: web:latest"}`))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"Id":"sha256:abc"}`))
				case strings.HasSuffix(r.URL.Path, "/images/create"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"Pulling from library/web","id":"latest"}` + "\n" +
						`{"errorDetail":{"message":"toomanyrequests"},"error":"toomanyrequests: rate limit"}` + "\n"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()

			proj := &composetypes.Project{Name: "demo", Services: composetypes.Services{
				"web": {Name: "web", Image: "web:latest", PullPolicy: "always"},
			}}
			err = (&Service{}).prePullImages(context.Background(), cli, proj, []string{"web"})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "toomanyrequests: rate limit") {
					t.Fatalf("err = %v, want the pull error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want fallback to the cached copy", err)
			}
		})
	}
}
