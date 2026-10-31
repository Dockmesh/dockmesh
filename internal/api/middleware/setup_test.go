package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireSetupHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header, form-style post", "", http.StatusForbidden},
		{"wizard header", "1", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireSetupHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
			r := httptest.NewRequest(http.MethodPost, "/api/v1/setup/commit", nil)
			r.Header.Set("Content-Type", "text/plain")
			if tt.header != "" {
				r.Header.Set(SetupHeader, tt.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d, want %d", w.Code, tt.want)
			}
		})
	}
}
