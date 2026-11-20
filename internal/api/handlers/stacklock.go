package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

// stackBusyResponse is the 409 body for a stack another operation holds.
type stackBusyResponse struct {
	Error     string    `json:"error"`
	Code      string    `json:"code"`
	Operation string    `json:"operation"`
	Since     time.Time `json:"since"`
}

// lockStack takes the stack lock for a user action. On a busy stack it
// answers 409 with the operation that holds it and returns ok=false.
func (h *Handlers) lockStack(w http.ResponseWriter, hostID, stack, op string) (release func(), ok bool) {
	release, err := h.StackLocks.TryLock(stacklock.HostKey(hostID, stack), op)
	if err != nil {
		writeStackBusy(w, err)
		return nil, false
	}
	return release, true
}

// writeStackBusy answers 409 when err says a stack is busy.
func writeStackBusy(w http.ResponseWriter, err error) bool {
	var busy *stacklock.BusyError
	if !errors.As(err, &busy) {
		return false
	}
	writeJSON(w, http.StatusConflict, stackBusyResponse{
		Error:     err.Error(),
		Code:      "stack_busy",
		Operation: busy.Operation,
		Since:     busy.Since.UTC(),
	})
	return true
}

// refuseMigrating answers 409 migration_active while stack has a
// migration that is not finished. Call it with the stack lock held: a
// migration takes the lock before it records itself, so either the
// lock or this check refuses.
func (h *Handlers) refuseMigrating(w http.ResponseWriter, r *http.Request, stack string) bool {
	if h.Migrations == nil {
		return false
	}
	active, err := h.Migrations.Store().HasActive(r.Context(), stack)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return true
	}
	if !active {
		return false
	}
	writeMigrationActive(w)
	return true
}

// writeMigrationConflict answers 409 for a stack another operation or an
// unfinished migration holds.
func writeMigrationConflict(w http.ResponseWriter, err error) bool {
	if errors.Is(err, migration.ErrMigrationActive) {
		writeMigrationActive(w)
		return true
	}
	return writeStackBusy(w, err)
}

// writeMigrationActive answers 409 migration_active with the text of
// migration.ErrMigrationActive.
func writeMigrationActive(w http.ResponseWriter) {
	writeError(w, http.StatusConflict, "the stack has a migration or migration rollback that has not finished; try again once it has")
}
