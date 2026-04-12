package handlers

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type Handlers struct {
	DB          *sql.DB
	Auth        *auth.Service
	Audit       *audit.Service
	Docker      *docker.Client // may be nil if the daemon was unreachable at startup
	Stacks      *stacks.Manager
	Compose     *compose.Service
	LoginLimter *ratelimit.Limiter
	Scanner     scanner.Scanner
	ScanStore   *scanner.Store
}

type Deps struct {
	DB           *sql.DB
	Auth         *auth.Service
	Audit        *audit.Service
	Docker       *docker.Client
	Stacks       *stacks.Manager
	Compose      *compose.Service
	LoginLimiter *ratelimit.Limiter
	Scanner      scanner.Scanner
	ScanStore    *scanner.Store
}

func New(d Deps) *Handlers {
	return &Handlers{
		DB:          d.DB,
		Auth:        d.Auth,
		Audit:       d.Audit,
		Docker:      d.Docker,
		Stacks:      d.Stacks,
		Compose:     d.Compose,
		LoginLimter: d.LoginLimiter,
		Scanner:     d.Scanner,
		ScanStore:   d.ScanStore,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("encode json", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
