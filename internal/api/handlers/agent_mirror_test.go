package handlers

import (
	"context"
	"testing"
	"time"
)

// A stack sync to an agent outlives the request that started it.
func TestMirrorContextOutlivesRequest(t *testing.T) {
	req, cancelReq := context.WithCancel(context.Background())
	ctx, cancel := mirrorContext(req)
	defer cancel()
	cancelReq()
	if err := ctx.Err(); err != nil {
		t.Fatalf("mirror context ended with the request: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > agentMirrorTimeout {
		t.Errorf("deadline = %v, %v; want within %v", deadline, ok, agentMirrorTimeout)
	}
}
