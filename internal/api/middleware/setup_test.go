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

func TestWizardOnlyPathAfterSetup(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/setup", true},
		{"/api/v1/setup/commit", true},
		{"/api/v1/setup/server-info", true},
		{"/api/v1/setup/status", false},
		{"/api/v1/setup/stream/0b9f0c1e-1d2a-4f7e-9c3b-8a1d2e3f4a5b", false},
		{"/api/v1/stacks", false},
	}
	for _, tt := range tests {
		if got := isWizardOnlyPath(tt.path); got != tt.want {
			t.Errorf("isWizardOnlyPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
