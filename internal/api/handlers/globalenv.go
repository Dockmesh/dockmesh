package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

// envRefRe matches docker-compose variable references: ${KEY}, ${KEY:-x},
// ${KEY:?x}, $KEY (no braces, less common). Captures the bare key name.
var envRefRe = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)(?::[-+?][^}]*)?\}|\$([A-Z_][A-Z0-9_]*)\b`)

// scanComposeRefs returns the set of env-var keys referenced anywhere
// in a compose file. Case-insensitive shell variables in docker-compose
// are still uppercase by convention; we don't try to handle lowercase
// or mixed-case (vanishingly rare in real-world stacks).
func scanComposeRefs(compose string) map[string]bool {
	keys := map[string]bool{}
	for _, m := range envRefRe.FindAllStringSubmatch(compose, -1) {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if k != "" {
			keys[k] = true
		}
	}
	return keys
}

// computeEnvRefMap walks every stack on disk and returns map[KEY] →
// list of stack names that reference it. Cheap enough to compute per
// request — the alternative is invalidation tracking on every stack
// edit, which is overkill for a UI hint.
func (h *Handlers) computeEnvRefMap() map[string][]string {
	out := map[string][]string{}
	if h.Stacks == nil {
		return out
	}
	for _, s := range h.Stacks.List() {
		detail, err := h.Stacks.Get(s.Name)
		if err != nil || detail == nil {
			continue
		}
		// scan compose + env (both can reference globals)
		combined := detail.Compose + "\n" + detail.Env
		for k := range scanComposeRefs(strings.ToUpper(combined)) {
			out[k] = append(out[k], s.Name)
		}
	}
	return out
}

// stackListInterface guards against nil stacks.Manager.
var _ = (*stacks.Manager)(nil)

func (h *Handlers) ListGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	vars, err := h.GlobalEnv.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vars == nil {
		vars = []globalenv.Var{}
	}
	// Enrich each var with refs_count — how many stacks reference it
	// via ${KEY} interpolation. The UI uses this for the "used by N
	// stacks" column + the "Used by" popover (see GET /global-env/
	// {id}/refs below for the stack-name list).
	refMap := h.computeEnvRefMap()
	type enriched struct {
		globalenv.Var
		RefsCount int `json:"refs_count"`
	}
	out := make([]enriched, len(vars))
	for i, v := range vars {
		out[i].Var = v
		out[i].RefsCount = len(refMap[v.Key])
	}
	writeJSON(w, http.StatusOK, out)
}

// GetGlobalEnvRefs returns the list of stack names that reference the
// given variable. Powers the UI's "Used by" popover.
//
//	GET /api/v1/global-env/{id}/refs
func (h *Handlers) GetGlobalEnvRefs(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	v, err := h.GlobalEnv.Get(r.Context(), id)
	if errors.Is(err, globalenv.ErrNotFound) {
		writeError(w, http.StatusNotFound, "variable not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	refMap := h.computeEnvRefMap()
	names := refMap[v.Key]
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":    v.Key,
		"stacks": names,
	})
}

func (h *Handlers) CreateGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	var in globalenv.VarInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	v, err := h.GlobalEnv.Create(r.Context(), in)
	if err != nil {
		if errors.Is(err, globalenv.ErrDuplicateKey) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *Handlers) UpdateGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in globalenv.VarInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	v, err := h.GlobalEnv.Update(r.Context(), id, in)
	if err != nil {
		if errors.Is(err, globalenv.ErrDuplicateKey) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handlers) DeleteGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.GlobalEnv.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ListGlobalEnvGroups(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	groups, err := h.GlobalEnv.Groups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
