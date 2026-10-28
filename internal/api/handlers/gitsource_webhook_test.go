package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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

const testWebhookSecret = "0123456789abcdef-secret"

func TestGitWebhook(t *testing.T) {
	h, gs, _ := newGitSourceTestHandlers(t)
	configureSource(t, gs, "signed", testWebhookSecret)
	configureSource(t, gs, "unsigned", "")

	var triggered []string
	h.gitWebhookTrigger = func(_ context.Context, stack string) {
		triggered = append(triggered, stack)
	}

	r := chi.NewRouter()
	r.Post("/stacks/{name}/git/webhook", h.GitWebhook)

	body := []byte(`{"ref":"refs/heads/main"}`)
	valid := "sha256=" + hmacHex(testWebhookSecret, body)
	tests := []struct {
		name        string
		stack       string
		headers     map[string]string
		want        int
		wantTrigger bool
	}{
		{"no secret configured", "unsigned", map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHex("", body)}, http.StatusUnauthorized, false},
		{"no signature", "signed", nil, http.StatusUnauthorized, false},
		{"wrong github signature", "signed", map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHex("other", body)}, http.StatusUnauthorized, false},
		{"wrong gitlab token", "signed", map[string]string{"X-Gitlab-Token": "other"}, http.StatusUnauthorized, false},
		{"unknown stack", "missing", map[string]string{"X-Hub-Signature-256": valid}, http.StatusUnauthorized, false},
		{"valid github signature", "signed", map[string]string{"X-Hub-Signature-256": valid}, http.StatusAccepted, true},
		{"valid gitea signature", "signed", map[string]string{"X-Gitea-Signature": hmacHex(testWebhookSecret, body)}, http.StatusAccepted, true},
		{"valid gitlab token", "signed", map[string]string{"X-Gitlab-Token": testWebhookSecret}, http.StatusAccepted, true},
	}
	var rejectedBody string
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			triggered = nil
			req := httptest.NewRequest(http.MethodPost, "/stacks/"+tc.stack+"/git/webhook", bytes.NewReader(body))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tc.want, rr.Body.String())
			}
			if rr.Code == http.StatusUnauthorized {
				if rejectedBody == "" {
					rejectedBody = rr.Body.String()
				} else if rr.Body.String() != rejectedBody {
					t.Fatalf("rejection body %q differs from %q", rr.Body.String(), rejectedBody)
				}
			}
			if got := len(triggered) == 1 && triggered[0] == tc.stack; got != tc.wantTrigger {
				t.Fatalf("triggered = %v, want trigger %v", triggered, tc.wantTrigger)
			}
		})
	}
}

func TestConfigureWebhookSecretLength(t *testing.T) {
	_, gs, _ := newGitSourceTestHandlers(t)
	ctx := context.Background()
	tests := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{"empty keeps webhook off", "", false},
		{"too short", "short-secret", true},
		{"long enough", testWebhookSecret, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := gitsource.Input{RepoURL: "https://example.com/repo.git", WebhookSecret: tc.secret}
			_, err := gs.Configure(ctx, "lengths", in)
			if gotErr := errors.Is(err, gitsource.ErrWebhookSecretShort); gotErr != tc.wantErr {
				t.Fatalf("err = %v, want short-secret error %v", err, tc.wantErr)
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
		{"source with secret", func() { configureSource(t, gs, "alpha", testWebhookSecret) }, false, ""},
		{"source without secret", func() { configureSource(t, gs, "beta", "") }, true, "beta"},
		{"secret added", func() { configureSource(t, gs, "beta", testWebhookSecret+"-new") }, false, ""},
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
			if !strings.Contains(n.Body, st.wantStack) {
				t.Fatalf("notice body %q does not name %q", n.Body, st.wantStack)
			}
			if n.Link != "/stacks/"+st.wantStack {
				t.Fatalf("notice link %q", n.Link)
			}
		})
	}
}
