package agents

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// EnrollPath is where the agent listener accepts enrollment requests.
// Serving enrollment there (instead of on the public API URL) means the
// TLS chain the agent sees is always signed by the agents CA, which the
// installer pins by fingerprint.
const EnrollPath = "/enroll"

const (
	maxEnrollBody     = 64 << 10
	enrollReadTimeout = 15 * time.Second
)

// EnrollURL is the https URL of the enrollment endpoint on the agent
// listener, derived from the public wss:// agent URL.
func (s *Service) EnrollURL() string {
	return enrollURLFromAgentURL(s.agentURL)
}

func enrollURLFromAgentURL(agentURL string) string {
	u, err := url.Parse(agentURL)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "wss", "https":
		u.Scheme = "https"
	case "ws", "http":
		u.Scheme = "http"
	default:
		return ""
	}
	u.Path = EnrollPath
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// EnrollHandler serves POST /enroll on the agent listener. The client has
// no certificate yet, so the listener must allow certificate-less
// handshakes; the one-time token is the credential.
func EnrollHandler(svc *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The listener accepts unauthenticated clients here, so a slow
		// body must not hold a connection open indefinitely.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(enrollReadTimeout))
		var req EnrollRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEnrollBody)).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		resp, err := svc.Enroll(r.Context(), req)
		if errors.Is(err, ErrInvalidToken) {
			http.Error(w, "invalid or expired enrollment token", http.StatusUnauthorized)
			return
		}
		if err != nil {
			slog.Error("agent enroll", "err", err)
			http.Error(w, "enrollment failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	})
}
