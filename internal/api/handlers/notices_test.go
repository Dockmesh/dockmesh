package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/go-chi/chi/v5"
)

func TestNoticesHandlers(t *testing.T) {
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	svc := notices.NewService(database)
	ctx := context.Background()
	if err := svc.Raise(ctx, notices.RaiseInput{Key: "all", Audience: notices.All(), Severity: notices.SevWarning, Title: i18n.Msg("for everyone")}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Raise(ctx, notices.RaiseInput{Key: "adm", Audience: notices.Admins(), Severity: notices.SevCritical, Title: i18n.Msg("admins")}); err != nil {
		t.Fatal(err)
	}

	h := &Handlers{Notices: svc}
	r := chi.NewRouter()
	r.Get("/notices", h.ListNotices)
	r.Post("/notices/{id}/dismiss", h.DismissNotice)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/notices", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status %d", rr.Code)
	}
	var got []notices.Notice
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "all" {
		t.Fatalf("anonymous caller must only see audience=all, got %+v", got)
	}

	open := strconv.FormatInt(got[0].ID, 10)
	token := middleware.Identity{APITokenID: 7, TokenOwnerID: "u1"}
	tests := []struct {
		name string
		id   string
		as   *middleware.Identity
		want int
	}{
		{"bad id", "x", nil, http.StatusBadRequest},
		{"no user session", open, nil, http.StatusNotFound},
		{"unknown", "999", nil, http.StatusNotFound},
		{"api token", open, &token, http.StatusForbidden},
		{"api token, unknown id", "999", &token, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/notices/"+tt.id+"/dismiss", nil)
			if tt.as != nil {
				req = req.WithContext(middleware.WithIdentity(req.Context(), *tt.as))
			}
			r.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status %d, want %d", rr.Code, tt.want)
			}
		})
	}
}
