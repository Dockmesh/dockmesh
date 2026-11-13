package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestUpdateCacheKeysAreNotSettable(t *testing.T) {
	var store *settings.Store
	authSvc, roles, h := newMatrixEnvWith(t, func(db *sql.DB, d *handlers.Deps) {
		store = settings.NewStore(db)
		d.Settings = store
	})
	ctx := context.Background()
	token := signIn(t, authSvc, roles, "u-admin", rbac.RoleAdmin, rbac.Global)
	router := NewRouter(h, authSvc, nil, true, nil)
	const realURL = "https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0"

	do := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	tests := []struct {
		name, key, value string
		wantCode         int
	}{
		{"script link", "update_release_url", "javascript:alert(1)", http.StatusBadRequest},
		{"other site", "update_release_url", "https://evil.example/releases/", http.StatusBadRequest},
		{"fake version", "update_latest_version", "v99.0.0", http.StatusBadRequest},
		{"fake notes", "update_release_notes", "run this", http.StatusBadRequest},
		{"fake manifest", "update_release_manifest", "{}", http.StatusBadRequest},
		{"unchanged value from an old page", "update_release_url", realURL, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range selfupdate.CacheKeys {
				if err := store.Set(ctx, k, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Set(ctx, "update_release_url", realURL); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal([]map[string]string{{"key": tt.key, "value": tt.value}, {"key": "update_check_enabled", "value": "false"}})
			rr := do(http.MethodPut, string(body))
			if rr.Code != tt.wantCode {
				t.Fatalf("status %d %s, want %d", rr.Code, rr.Body, tt.wantCode)
			}
			want := ""
			if tt.key == "update_release_url" {
				want = realURL
			}
			if got := store.Stored(tt.key); got != want {
				t.Errorf("%s = %q, want %q", tt.key, got, want)
			}
			if tt.wantCode == http.StatusOK && store.Stored("update_check_enabled") != "false" {
				t.Error("the other setting in the request was not saved")
			}
			if tt.wantCode != http.StatusOK && store.Stored("update_check_enabled") == "false" {
				t.Error("a refused request saved its other settings")
			}
			if err := store.Set(ctx, "update_check_enabled", ""); err != nil {
				t.Fatal(err)
			}
		})
	}

	rr := do(http.MethodGet, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var list []settings.Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if selfupdate.IsCacheKey(e.Key) {
			t.Errorf("settings list shows the cache key %s", e.Key)
		}
	}
}
