package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestInviteCreatedByAPITokenCanBeAccepted(t *testing.T) {
	e := newScopeEnv(t)
	owner := e.user("boss", "admin")
	token := e.apiToken(e.login("boss").AccessToken, "admin")

	rr := e.do(http.MethodPost, "/invites", token, map[string]any{"role": "viewer"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var inv struct {
		Token     string `json:"token"`
		CreatedBy string `json:"created_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	stored, err := e.h.Invites.List(context.Background(), 10)
	if err != nil || len(stored) != 1 || stored[0].CreatedBy != owner.ID {
		t.Fatalf("stored inviter = %+v, %v; want %s", stored, err, owner.ID)
	}

	accept := chiRouterFor(http.MethodPost, "/invite/{token}/accept", e.h.AcceptInvite)
	req := httptest.NewRequest(http.MethodPost, "/invite/"+inv.Token+"/accept",
		strings.NewReader(`{"username":"newbie","password":"`+testPassword+`"}`))
	got := httptest.NewRecorder()
	accept.ServeHTTP(got, req)
	if got.Code != http.StatusCreated {
		t.Fatalf("accept: %d %s", got.Code, got.Body)
	}
}

type readyScanner struct {
	scanner.Scanner
	err error
}

func (s readyScanner) Ready() error { return s.err }

func TestScannerHealthFollowsActualState(t *testing.T) {
	missing := errors.New("no binary")
	tests := []struct {
		name       string
		setting    string
		scan       scanner.Scanner
		wantStatus string
		wantDetail string
	}{
		{"on and ready", "true", readyScanner{}, "ok", "Grype ready"},
		{"on without binary", "true", readyScanner{err: missing}, "off", "enabled, but the Grype binary was not found"},
		{"off with binary", "false", readyScanner{}, "off", "disabled"},
		{"on without scanner", "true", nil, "off", "not configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthzEnv(t)
			store := settings.NewStore(e.db)
			if err := store.Set(context.Background(), settings.KeyScannerEnabled, tt.setting); err != nil {
				t.Fatal(err)
			}
			e.h.Settings, e.h.Scanner = store, tt.scan
			got := e.h.scannerHealth()
			if got.Status != tt.wantStatus || got.Detail != tt.wantDetail {
				t.Fatalf("health = %s %q, want %s %q", got.Status, got.Detail, tt.wantStatus, tt.wantDetail)
			}
		})
	}
}

func TestScannerSettingAppliesWithoutRestart(t *testing.T) {
	e := newImageHostEnv(t)
	store := settings.NewStore(e.db)
	e.h.Settings = store
	ctx := context.Background()
	scan := func() int {
		t.Helper()
		return e.do(http.MethodPost, hostQuery("/images/nginx:1/scan", "local"), e.boss, nil).Code
	}
	if err := store.Set(ctx, settings.KeyScannerEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if code := scan(); code != http.StatusServiceUnavailable {
		t.Fatalf("scan while off: %d, want 503", code)
	}
	if err := store.Set(ctx, settings.KeyScannerEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	if code := scan(); code != http.StatusOK {
		t.Fatalf("scan after turning on: %d, want 200", code)
	}
}
