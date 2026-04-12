package handlers

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type Handlers struct {
	DB      *sql.DB
	Auth    *auth.Service
	Docker  *docker.Client // may be nil if the daemon was unreachable at startup
	Stacks  *stacks.Manager
	Compose *compose.Service
}

func New(db *sql.DB, authSvc *auth.Service, dockerCli *docker.Client, stacksMgr *stacks.Manager, composeSvc *compose.Service) *Handlers {
	return &Handlers{DB: db, Auth: authSvc, Docker: dockerCli, Stacks: stacksMgr, Compose: composeSvc}
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
