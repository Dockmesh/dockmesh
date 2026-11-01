package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// BackupHost is a host the caller may create or edit backup jobs on.
type BackupHost struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	CanCreate bool   `json:"can_create"`
	CanUpdate bool   `json:"can_update"`
}

// ListBackupHosts lists the hosts the job wizard offers: those where the
// caller holds backups.create or backups.update on the host itself.
//
//	GET /api/v1/backups/hosts
func (h *Handlers) ListBackupHosts(w http.ResponseWriter, r *http.Request) {
	out := []BackupHost{}
	if h.Hosts == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	list, err := h.Hosts.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	acc := middleware.AccessOf(r.Context())
	for _, hi := range list {
		res := h.hostScopeReq(hi.ID)
		bh := BackupHost{
			ID: hi.ID, Name: hi.Name, Kind: hi.Kind, Status: hi.Status,
			CanCreate: acc.AllowsOn(rbac.PermBackupsCreate, res),
			CanUpdate: acc.AllowsOn(rbac.PermBackupsUpdate, res),
		}
		if bh.CanCreate || bh.CanUpdate {
			out = append(out, bh)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func jobHostID(id string) string {
	if id == "" {
		return "local"
	}
	return id
}

// jobPlacementChanged reports whether a write moves the job to another
// host or source. Jobs whose placement stays put skip the existence
// checks, so an edit of a job on an offline agent still saves.
func jobPlacementChanged(prev *backup.Job, in backup.JobInput) bool {
	if prev == nil {
		return true
	}
	return jobHostID(prev.HostID) != jobHostID(in.HostID) || !slices.Equal(prev.Sources, in.Sources)
}

// deriveStackJobHost sets the host of a stack job written without one to
// the host the stack is deployed on. A stack the caller can't manage
// backups of keeps the server host, so the answer looks the same as for a
// stack that doesn't exist.
func (h *Handlers) deriveStackJobHost(r *http.Request, prev *backup.Job, in *backup.JobInput) error {
	if in.HostID != "" || len(in.Sources) != 1 || in.Sources[0].Type != "stack" || h.Deployments == nil {
		return nil
	}
	if !jobPlacementChanged(prev, *in) {
		return nil
	}
	stack := in.Sources[0].Name
	dep, err := h.Deployments.Get(r.Context(), stack)
	if err != nil {
		return err
	}
	if dep != nil && dep.HostID != "" && h.allowedOn(r, h.stackScopeReq(dep.HostID, stack)) {
		in.HostID = dep.HostID
	}
	return nil
}

// checkBackupJobPlacement validates the job's host and source: the host
// must be known, a system job runs on the server, a stack job on the host
// the stack is deployed on, and a volume must exist on the job's host.
// Call it after the scope checks, so its answers say nothing about hosts
// or volumes outside the caller's scope.
func (h *Handlers) checkBackupJobPlacement(w http.ResponseWriter, r *http.Request, prev *backup.Job, in backup.JobInput) bool {
	if !jobPlacementChanged(prev, in) {
		return true
	}
	hostID := jobHostID(in.HostID)
	if backup.HasSystemSource(in.Sources) {
		if hostID != "local" {
			writeError(w, http.StatusBadRequest, "system backups run on the server host only")
			return false
		}
		return true
	}
	known, err := h.knownHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !known {
		writeError(w, http.StatusBadRequest, "unknown host "+hostID)
		return false
	}
	for _, src := range in.Sources {
		switch src.Type {
		case "stack":
			if msg := h.stackJobProblem(r, src.Name, hostID); msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return false
			}
		case "volume":
			if err := docker.CheckVolumeName(src.Name); err != nil {
				writeError(w, http.StatusBadRequest, "volume source must be a Docker volume name: "+err.Error())
				return false
			}
			if err := h.checkBackupVolume(r.Context(), hostID, src.Name); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return false
			}
		default:
			writeError(w, http.StatusBadRequest, "unknown source type "+src.Type)
			return false
		}
	}
	return true
}

func (h *Handlers) knownHost(ctx context.Context, hostID string) (bool, error) {
	if h.Hosts == nil {
		return hostID == "local", nil
	}
	list, err := h.Hosts.List(ctx)
	if err != nil {
		return false, err
	}
	for _, hi := range list {
		if hi.ID == hostID {
			return true, nil
		}
	}
	return false, nil
}

// stackJobProblem explains why a stack can't be backed up on hostID, or
// returns "" when it can. Stacks deployed where the caller can't manage
// backups get the same answer as missing ones.
func (h *Handlers) stackJobProblem(r *http.Request, stack, hostID string) string {
	notHere := "stack " + stack + " is not deployed on host " + hostID
	if h.Stacks != nil {
		if _, err := h.Stacks.Get(stack); err != nil {
			return notHere
		}
	}
	if h.Deployments == nil {
		return ""
	}
	dep, err := h.Deployments.Get(r.Context(), stack)
	if err != nil {
		slog.Warn("backup job stack deployment", "stack", stack, "err", err)
		return notHere
	}
	switch {
	case dep == nil || dep.HostID == "":
		if h.allowedOn(r, h.stackScopeReq("", stack)) {
			return notHere + "; deploy it before backing it up"
		}
		return notHere
	case dep.HostID == hostID:
		return ""
	case h.allowedOn(r, h.stackScopeReq(dep.HostID, stack)):
		return fmt.Sprintf("stack %s is deployed on host %s; its backup job has to run there", stack, dep.HostID)
	default:
		return notHere
	}
}

func (h *Handlers) checkBackupVolume(ctx context.Context, hostID, name string) error {
	if h.backupVolumeCheck != nil {
		return h.backupVolumeCheck(ctx, hostID, name)
	}
	if h.Hosts == nil {
		return errors.New("hosts not configured")
	}
	target, err := h.Hosts.Pick(hostID)
	if errors.Is(err, host.ErrAgentOffline) {
		return fmt.Errorf("host %s is offline; its volumes can't be checked", hostID)
	}
	if err != nil {
		return err
	}
	if _, err := target.InspectVolume(ctx, name); err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("volume %s not found on host %s", name, hostID)
		}
		slog.Warn("backup job volume check", "host", hostID, "volume", name, "err", err)
		return fmt.Errorf("can't check volume %s on host %s", name, hostID)
	}
	return nil
}
