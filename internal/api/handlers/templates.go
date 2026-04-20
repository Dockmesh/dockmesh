package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListTemplates(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeJSON(w, http.StatusOK, []templates.Template{})
		return
	}
	list, err := h.Templates.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) GetTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := h.Templates.Get(r.Context(), id)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	var in templates.Input
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, err := h.Templates.Create(r.Context(), in)
	if errors.Is(err, templates.ErrDuplicateSlug) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "template.create", t.Slug, nil)
	writeJSON(w, http.StatusCreated, t)
}

func (h *Handlers) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in templates.Input
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	t, err := h.Templates.Update(r.Context(), id, in)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, templates.ErrBuiltinImmutable) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, templates.ErrDuplicateSlug) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "template.update", t.Slug, nil)
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Templates.Delete(r.Context(), id); err != nil {
		if errors.Is(err, templates.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, templates.ErrBuiltinImmutable) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "template.delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// DeployTemplate renders the template with the supplied values
// (auto-generating secrets for any `secret: true` parameter not
// provided), creates a stack on the FS from the rendered compose, and
// deploys it against the target host. Returns the new stack name + the
// compose snapshot so the UI can show what was deployed.
func (h *Handlers) DeployTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req templates.DeployRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.StackName == "" {
		writeError(w, http.StatusBadRequest, "stack_name is required")
		return
	}
	if err := stacks.ValidateName(req.StackName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tpl, err := h.Templates.Get(r.Context(), id)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	composeYAML, envContent, resolved, err := h.Templates.Materialize(r.Context(), id, req.Values)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Write the stack files — same path as POST /stacks.
	if _, err := h.Stacks.Create(req.StackName, composeYAML, envContent); err != nil {
		if errors.Is(err, stacks.ErrExists) {
			writeError(w, http.StatusConflict, "stack already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Deploy on the target host.
	target, err := h.resolveHost(req.HostID)
	if err != nil {
		// Clean up the stack we just wrote so a failed deploy doesn't
		// leave an orphaned stack behind.
		_ = h.Stacks.Delete(req.StackName)
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	deployRes, err := target.DeployStack(r.Context(), req.StackName, composeYAML, h.mergeGlobalEnv(r.Context(), envContent))
	if err != nil {
		// Leave the stack on disk — the operator might want to
		// fix the compose and retry via /stacks/{name}/deploy.
		// Classify user-fixable errors (yaml parse, port in use, pull
		// auth, etc.) as 422 instead of the generic 502.
		writeError(w, deployErrorStatus(err), friendlyDeployError(err))
		return
	}

	// Echo back the resolved values with secret params redacted so the
	// caller has a record without leaking auto-generated passwords.
	secretNames := make(map[string]bool, len(tpl.Parameters))
	for _, p := range tpl.Parameters {
		if p.Secret {
			secretNames[p.Name] = true
		}
	}
	safe := make(map[string]string, len(resolved))
	for k, v := range resolved {
		if secretNames[k] {
			safe[k] = "<generated>"
		} else {
			safe[k] = v
		}
	}

	h.audit(r, "template.deploy", req.StackName,
		map[string]any{"template_id": id, "host": target.ID()})
	writeJSON(w, http.StatusCreated, map[string]any{
		"stack":        req.StackName,
		"compose":      composeYAML,
		"values":       safe,
		"deploy_result": deployRes,
	})
}

func (h *Handlers) ExportTemplate(w http.ResponseWriter, r *http.Request) {
	if h.Templates == nil {
		writeError(w, http.StatusServiceUnavailable, "templates not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	body, err := h.Templates.Export(r.Context(), id)
	if errors.Is(err, templates.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=template.yaml")
	_, _ = w.Write(body)
}

// resolveHost picks the target host by id. Empty / "local" returns the
// central daemon; otherwise falls back to h.pickHost's logic.
func (h *Handlers) resolveHost(id string) (host.Host, error) {
	if h.Hosts == nil {
		if h.Docker == nil {
			return nil, host.ErrNoDocker
		}
		return host.NewLocal(h.Docker), nil
	}
	return h.Hosts.Pick(id)
}

