package handlers

import (
	"io"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// maxAvatarBytes caps the upload to 1 MiB — Account avatars never need
// more than a tiny PNG/JPG/WebP. The limit applies to the raw payload;
// no resizing or re-encoding happens server-side (keep the server
// stateless about image libs).
const maxAvatarBytes = 1 << 20

// avatarMimeAllowed lists the MIME types accepted by SetAvatar. Limited
// on purpose — accepting SVG would open us to JS-in-XML browser quirks
// when the blob is served back via GET /users/{id}/avatar.
var avatarMimeAllowed = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// UploadAvatar accepts a single-field multipart upload ("avatar") or
// a raw body when the request Content-Type matches an allowed MIME.
// The latter is friendlier to scripts that want to curl an avatar.
//
//	POST /api/v1/users/{id}/avatar
//	(multipart/form-data with field "avatar", or
//	 raw image bytes with Content-Type: image/png|jpeg|webp)
func (h *Handlers) UploadAvatar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canManageUser(r, id) {
		writeError(w, http.StatusForbidden, "you can only change your own avatar")
		return
	}

	// Cap the body length up front; ReadFrom won't grow past this.
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+1024)

	var (
		data []byte
		mime string
	)
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(maxAvatarBytes + 1024); err != nil {
			writeError(w, http.StatusBadRequest, "could not parse upload (size limit "+humanBytes(maxAvatarBytes)+")")
			return
		}
		file, header, err := r.FormFile("avatar")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing 'avatar' form field")
			return
		}
		defer file.Close()
		buf, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(buf) > maxAvatarBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "avatar exceeds 1 MiB")
			return
		}
		data = buf
		mime = header.Header.Get("Content-Type")
	case avatarMimeAllowed[ct]:
		buf, err := io.ReadAll(io.LimitReader(r.Body, maxAvatarBytes+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if len(buf) > maxAvatarBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "avatar exceeds 1 MiB")
			return
		}
		data = buf
		mime = ct
	default:
		writeError(w, http.StatusBadRequest, "upload as multipart/form-data with field 'avatar', or send raw bytes with Content-Type: image/png|jpeg|webp")
		return
	}

	if !avatarMimeAllowed[mime] {
		writeError(w, http.StatusUnsupportedMediaType, "avatar must be image/png, image/jpeg, or image/webp")
		return
	}
	if err := h.Auth.SetAvatar(r.Context(), id, mime, data); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.avatar_update", id, map[string]any{
		"mime": mime, "bytes": len(data),
	})
	w.WriteHeader(http.StatusNoContent)
}

// GetAvatar serves the avatar bytes. Public-by-session — any
// authenticated user can fetch any other user's avatar (matches the
// list-users semantic: the existence of the user is already visible).
//
//	GET /api/v1/users/{id}/avatar
func (h *Handlers) GetAvatar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	data, mime, err := h.Auth.GetAvatar(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mime == "" || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(data)
}

// DeleteAvatar clears the user's avatar.
//
//	DELETE /api/v1/users/{id}/avatar
func (h *Handlers) DeleteAvatar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canManageUser(r, id) {
		writeError(w, http.StatusForbidden, "you can only change your own avatar")
		return
	}
	if err := h.Auth.SetAvatar(r.Context(), id, "", nil); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.avatar_delete", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// canManageUser is the "self or admin" gate used by avatar + later
// account-self-edit endpoints. Self matches by JWT subject; the admin
// override needs users.update.
func (h *Handlers) canManageUser(r *http.Request, targetID string) bool {
	uid := middleware.UserID(r.Context())
	if uid != "" && uid == targetID {
		return true
	}
	role := middleware.Role(r.Context())
	if h.Roles != nil {
		if rd, ok := h.Roles.Get(role); ok {
			for _, p := range rd.Permissions {
				if p == rbac.PermUsersUpdate {
					return true
				}
			}
			return false
		}
	}
	return rbac.Allowed(role, rbac.PermUsersUpdate)
}

func humanBytes(n int) string {
	if n < 1024 {
		return "small"
	}
	if n < 1<<20 {
		return "<1 KiB"
	}
	return "1 MiB"
}
