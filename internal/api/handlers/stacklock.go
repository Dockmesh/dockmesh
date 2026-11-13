package handlers

import (
	"errors"
	"net/http"
	"time"

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
