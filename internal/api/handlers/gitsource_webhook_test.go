package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/go-chi/chi/v5"
)

func newGitSourceTestHandlers(t *testing.T) (*Handlers, *gitsource.Service, *notices.Service) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	gs := gitsource.New(database, nil, nil, t.TempDir(), nil)
	ns := notices.NewService(database)
	return &Handlers{GitSource: gs, Notices: ns}, gs, ns
}

func configureSource(t *testing.T, gs *gitsource.Service, stack, secret string) {
	t.Helper()
	in := gitsource.Input{RepoURL: "https://example.com/" + stack + ".git", WebhookSecret: secret}
	if _, err := gs.Configure(context.Background(), stack, in); err != nil {
		t.Fatal(err)
	}
}

func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestGitWebhook(t *testing.T) {
	h, gs, _ := newGitSourceTestHandlers(t)
	configureSource(t, gs, "signed", "s3cret")
	configureSource(t, gs, "unsigned", "")

	type syncCall struct {
		stack string
		ctx   context.Context
	}
	calls := make(chan syncCall, 4)
	release := make(chan struct{})
	h.gitWebhookSync = func(ctx context.Context, stack string) error {
		calls <- syncCall{stack, ctx}
		<-release
		return nil
	}

	r := chi.NewRouter()
	r.Post("/stacks/{name}/git/webhook", h.GitWebhook)

	body := []byte(`{"ref":"refs/heads/main"}`)
	tests := []struct {
		name     string
		stack    string
		headers  map[string]string
		want     int
		wantSync bool
	}{
		{"no secret configured", "unsigned", map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHex("", body)}, http.StatusForbidden, false},
		{"no signature", "signed", nil, http.StatusUnauthorized, false},
		{"wrong github signature", "signed", map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHex("other", body)}, http.StatusUnauthorized, false},
		{"wrong gitlab token", "signed", map[string]string{"X-Gitlab-Token": "other"}, http.StatusUnauthorized, false},
		{"unknown stack", "missing", map[string]string{"X-Gitlab-Token": "s3cret"}, http.StatusNotFound, false},
		{"valid github signature", "signed", map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHex("s3cret", body)}, http.StatusAccepted, true},
		{"valid gitea signature", "signed", map[string]string{"X-Gitea-Signature": hmacHex("s3cret", body)}, http.StatusAccepted, true},
		{"valid gitlab token", "signed", map[string]string{"X-Gitlab-Token": "s3cret"}, http.StatusAccepted, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx, cancelReq := context.WithCancel(context.Background())
			req := httptest.NewRequest(http.MethodPost, "/stacks/"+tc.stack+"/git/webhook", bytes.NewReader(body)).WithContext(reqCtx)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			cancelReq()
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tc.want, rr.Body.String())
			}
			if !tc.wantSync {
				select {
				case c := <-calls:
					t.Fatalf("unexpected sync of %q", c.stack)
				case <-time.After(50 * time.Millisecond):
				}
				return
			}
			select {
			case c := <-calls:
				if c.stack != tc.stack {
					t.Fatalf("synced %q, want %q", c.stack, tc.stack)
				}
				if err := c.ctx.Err(); err != nil {
					t.Fatalf("sync context ended with the request: %v", err)
				}
				if _, ok := c.ctx.Deadline(); !ok {
					t.Fatal("sync context has no timeout")
				}
				release <- struct{}{}
			case <-time.After(2 * time.Second):
				t.Fatal("sync was not started")
			}
		})
	}
}

func TestGitWebhookNotice(t *testing.T) {
	h, gs, ns := newGitSourceTestHandlers(t)
	ctx := context.Background()

	open := func() *notices.Notice {
		t.Helper()
		list, err := ns.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range list {
			if list[i].Key == "git-webhook.no-secret" {
				return &list[i]
			}
		}
		return nil
	}

	steps := []struct {
		name      string
		apply     func()
		wantOpen  bool
		wantStack string
	}{
		{"no git sources", func() {}, false, ""},
		{"source with secret", func() { configureSource(t, gs, "alpha", "s3cret") }, false, ""},
		{"source without secret", func() { configureSource(t, gs, "beta", "") }, true, "beta"},
		{"secret added", func() { configureSource(t, gs, "beta", "n3w") }, false, ""},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			st.apply()
			h.syncGitWebhookNotice(ctx)
			n := open()
			if (n != nil) != st.wantOpen {
				t.Fatalf("notice open = %v, want %v", n != nil, st.wantOpen)
			}
			if n == nil {
				return
			}
			if !bytes.Contains([]byte(n.Body), []byte(st.wantStack)) {
				t.Fatalf("notice body %q does not name %q", n.Body, st.wantStack)
			}
			if n.Link != "/stacks/"+st.wantStack {
				t.Fatalf("notice link %q", n.Link)
			}
		})
	}
}
