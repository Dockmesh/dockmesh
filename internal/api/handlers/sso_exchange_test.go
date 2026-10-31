package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
)

func ssoExchangeReq(code, cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sso/exchange", strings.NewReader(`{"code":"`+code+`"}`))
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: ssoCodeCookie, Value: cookie})
	}
	return r
}

func TestSSOExchange(t *testing.T) {
	login := &auth.LoginResult{AccessToken: "a", RefreshToken: "r"}
	tests := []struct {
		name   string
		cookie func(code string) string
		redeem int
		want   []int
	}{
		{"with matching cookie", func(c string) string { return c }, 2, []int{200, 401}},
		{"no cookie (planted link)", func(string) string { return "" }, 1, []int{401}},
		{"cookie of another code", func(string) string { return "other" }, 1, []int{401}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handlers{}
			code, ok := h.ssoCodes.put(login)
			if !ok {
				t.Fatal("put failed")
			}
			for i, want := range tt.want {
				w := httptest.NewRecorder()
				h.SSOExchange(w, ssoExchangeReq(code, tt.cookie(code)))
				if w.Code != want {
					t.Fatalf("attempt %d: got %d, want %d", i, w.Code, want)
				}
			}
		})
	}
}

func TestSSOExchangeExpired(t *testing.T) {
	h := &Handlers{}
	code, _ := h.ssoCodes.put(&auth.LoginResult{AccessToken: "a"})
	h.ssoCodes.mu.Lock()
	e := h.ssoCodes.entries[code]
	e.expires = time.Now().Add(-time.Second)
	h.ssoCodes.entries[code] = e
	h.ssoCodes.mu.Unlock()
	w := httptest.NewRecorder()
	h.SSOExchange(w, ssoExchangeReq(code, code))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired code: got %d", w.Code)
	}
}

func TestFinishSSOLoginNoTokensInURL(t *testing.T) {
	h := &Handlers{}
	w := httptest.NewRecorder()
	h.finishSSOLogin(w, httptest.NewRequest(http.MethodGet, "/cb", nil), &auth.LoginResult{AccessToken: "SECRETA", RefreshToken: "SECRETR"})
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?sso_code=") || strings.Contains(loc, "SECRET") || strings.Contains(loc, "#") {
		t.Fatalf("unexpected redirect %q", loc)
	}
}
