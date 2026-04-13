package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// chiParam wraps chi.URLParam so install.go can stay free of the chi
// import (helps keep the import surface obvious in code review).
func chiParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}
