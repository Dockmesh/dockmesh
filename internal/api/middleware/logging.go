package middleware

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// PromRecorder is what Logging uses to increment the prom request
// counter. Decoupled via interface so middleware stays import-light
// and avoids a cycle with the metrics package.
type PromRecorder interface {
	IncAPIRequest(method, pathPattern, status string)
}

// PromMetrics is set from main() after the prom collector is built.
// Nil-safe: the middleware skips emission when unset.
var PromMetrics PromRecorder

func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		status := ww.Status()
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
			"req_id", chimw.GetReqID(r.Context()),
		)
		if PromMetrics != nil {
			// RoutePattern gives the chi route template
			// ("/api/v1/containers/{id}") which keeps cardinality
			// bounded — the raw URL would blow up the counter.
			pattern := chi.RouteContext(r.Context()).RoutePattern()
			if pattern == "" {
				pattern = "unknown"
			}
			PromMetrics.IncAPIRequest(r.Method, pattern, strconv.Itoa(status))
		}
	})
}
