package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/changelog"
	"github.com/dockmesh/dockmesh/internal/userprefs"
	"github.com/dockmesh/dockmesh/pkg/version"
)

const whatsNewChangelog = `## [Unreleased]

### Added
- Not released yet.

## [0.4.2] - 2026-10-30 [recommended]

### Highlights
- Updates keep your volumes.

### Added
- Release notes in the app.

### Fixed
- A fix.

### Security
- GHSA-2xq8-c4fv-9m7w (high): An advisory.
`

func TestWhatsNew(t *testing.T) {
	e := newAuthzEnv(t)
	sections, err := changelog.Parse(whatsNewChangelog)
	if err != nil {
		t.Fatal(err)
	}
	e.h.releaseSections = sections
	e.h.UserPrefs = userprefs.New(e.db)
	auth := middleware.NewAuth(e.h.Auth)
	e.router.With(auth).Get("/system/whats-new", e.h.GetWhatsNew)
	e.router.With(auth).Post("/system/whats-new/seen", e.h.MarkWhatsNewSeen)
	e.user("ann", "viewer")
	tok := e.login("ann").AccessToken

	prev := version.Version
	t.Cleanup(func() { version.Version = prev })

	get := func() WhatsNew {
		t.Helper()
		rr := e.do(http.MethodGet, "/system/whats-new", tok, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET: %d %s", rr.Code, rr.Body)
		}
		var out WhatsNew
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	version.Version = "v0.4.2"
	got := get()
	if got.Current == nil || got.Current.Version != "0.4.2" {
		t.Fatalf("current = %+v", got.Current)
	}
	if len(got.Current.Highlights) != 1 || len(got.Current.Added) != 1 || len(got.Current.Fixed) != 1 || len(got.Current.Changed) != 0 {
		t.Errorf("notes = %+v", got.Current)
	}
	if len(got.Current.Security) != 1 || got.Current.Security[0] != "GHSA-2xq8-c4fv-9m7w (high): An advisory." {
		t.Errorf("security = %q", got.Current.Security)
	}
	if !got.Unseen || got.LastSeenVersion != "" || got.Latest != nil {
		t.Errorf("before seen: %+v", got)
	}
	if rr := e.do(http.MethodPost, "/system/whats-new/seen", tok, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("seen: %d %s", rr.Code, rr.Body)
	}
	if got := get(); got.Unseen || got.LastSeenVersion != "0.4.2" {
		t.Errorf("after seen: %+v", got)
	}

	version.Version = "v0.4.3"
	if got := get(); got.Current != nil || got.Unseen {
		t.Errorf("version without a section: %+v", got)
	}
	version.Version = "dev"
	if got := get(); got.Current != nil {
		t.Errorf("dev build shows %+v", got.Current)
	}
	if rr := e.do(http.MethodPost, "/system/whats-new/seen", tok, nil); rr.Code != http.StatusConflict {
		t.Errorf("seen on dev build: %d, want 409", rr.Code)
	}

	version.Version = "v0.4.2"
	apiTok := e.apiToken(e.login("ann").AccessToken, "viewer")
	if rr := e.do(http.MethodPost, "/system/whats-new/seen", apiTok, nil); rr.Code != http.StatusNotFound {
		t.Errorf("seen with API token: %d, want 404", rr.Code)
	}
}
