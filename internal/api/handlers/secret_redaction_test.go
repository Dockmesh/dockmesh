package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/go-chi/chi/v5"
)

func openRedactionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestSettingsHideAuditWebhookKeys(t *testing.T) {
	store := settings.NewStore(openRedactionTestDB(t))
	ctx := context.Background()
	for k, v := range map[string]string{
		audit.WebhookURLKey:    "https://siem.example.com/hook?token=abc",
		audit.WebhookSecretKey: "hmac-topsecret",
		"base_url":             "https://dockmesh.example.com",
	} {
		if err := store.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handlers{Settings: store}
	r := chi.NewRouter()
	r.Get("/settings", h.ListSettings)
	r.Put("/settings", h.UpdateSettings)

	tests := []struct {
		name     string
		method   string
		body     string
		wantCode int
	}{
		{"list", http.MethodGet, "", http.StatusOK},
		{"update other key", http.MethodPut, `[{"key":"base_url","value":"https://x.example.com"}]`, http.StatusOK},
		{"write webhook secret", http.MethodPut, `[{"key":"audit.webhook_hmac_secret","value":"evil"}]`, http.StatusBadRequest},
		{"write webhook url", http.MethodPut, `[{"key":"audit.webhook_url","value":"https://evil.example.com"}]`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(tc.method, "/settings", strings.NewReader(tc.body)))
			if rr.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			body := rr.Body.String()
			if strings.Contains(body, "hmac-topsecret") || strings.Contains(body, "token=abc") {
				t.Fatalf("response leaks webhook config: %s", body)
			}
			if rr.Code == http.StatusOK && !strings.Contains(body, "base_url") {
				t.Fatalf("response dropped unrelated settings: %s", body)
			}
		})
	}
	if got := store.Get(audit.WebhookSecretKey, ""); got != "hmac-topsecret" {
		t.Fatalf("stored secret changed to %q", got)
	}
	if got := store.Get(audit.WebhookURLKey, ""); got != "https://siem.example.com/hook?token=abc" {
		t.Fatalf("stored url changed to %q", got)
	}
}

func TestGlobalEnvRedactsSecretValues(t *testing.T) {
	store := globalenv.NewStore(openRedactionTestDB(t))
	ctx := context.Background()
	secret, err := store.Create(ctx, globalenv.VarInput{Key: "DB_PASSWORD", Value: "s3cr3t-pw", Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := store.Create(ctx, globalenv.VarInput{Key: "BASE_DOMAIN", Value: "haus.lan"})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handlers{GlobalEnv: store}
	r := chi.NewRouter()
	r.Get("/global-env", h.ListGlobalEnv)
	r.Post("/global-env", h.CreateGlobalEnv)
	r.Put("/global-env/{id}", h.UpdateGlobalEnv)

	call := func(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(method, path, &buf))
		if rr.Code >= 300 {
			t.Fatalf("%s %s: status %d: %s", method, path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "s3cr3t") {
			t.Fatalf("%s %s leaks the secret value: %s", method, path, rr.Body.String())
		}
		return rr
	}
	secretPath := "/global-env/" + strconv.FormatInt(secret.ID, 10)

	rr := call(t, http.MethodGet, "/global-env", nil)
	if !strings.Contains(rr.Body.String(), "haus.lan") {
		t.Fatalf("plain value must stay visible: %s", rr.Body.String())
	}
	call(t, http.MethodPost, "/global-env", globalenv.VarInput{Key: "API_TOKEN", Value: "s3cr3t-token", Encrypted: true})

	tests := []struct {
		name      string
		path      string
		in        globalenv.VarInput
		wantValue string
		wantEnc   bool
	}{
		{"empty value keeps secret", secretPath, globalenv.VarInput{Key: "DB_PASSWORD", Group: "db"}, "s3cr3t-pw", true},
		{"new value replaces secret", secretPath, globalenv.VarInput{Key: "DB_PASSWORD", Value: "s3cr3t-new", Encrypted: true}, "s3cr3t-new", true},
		{"empty plain value is stored", "/global-env/" + strconv.FormatInt(plain.ID, 10), globalenv.VarInput{Key: "BASE_DOMAIN"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call(t, http.MethodPut, tc.path, tc.in)
			id, _ := strconv.ParseInt(tc.path[strings.LastIndex(tc.path, "/")+1:], 10, 64)
			got, err := store.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Value != tc.wantValue || got.Encrypted != tc.wantEnc {
				t.Fatalf("stored %q encrypted=%v, want %q encrypted=%v", got.Value, got.Encrypted, tc.wantValue, tc.wantEnc)
			}
		})
	}
}
