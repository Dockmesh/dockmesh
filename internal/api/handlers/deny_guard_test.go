package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Handler-level permission refusals go through denyPerm or
// writeScopeDenied so they are audited. The listed messages answer 403
// for something other than a missing permission (ownership, a refusal
// that applies to everyone, a legacy token, a ticket for another
// permission) or are audited right before.
func TestForbiddenGoesThroughDenyPerm(t *testing.T) {
	allowed := []string{
		`"you can only change your own avatar"`,
		`"not your notification"`,
		`"the file browser is not available for Dockmesh's own containers"`,
		`"legacy API tokens can't create tokens; sign in, or use a token created after the upgrade"`,
		`"out of scope: none of your assignments allows this here"`,
		`"choose the host this stack is for: you can only create stacks on some hosts"`,
		`"ticket not scoped for "`,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inDenyPerm := false
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "func ") {
				inDenyPerm = strings.HasPrefix(line, "func (h *Handlers) denyPerm(")
			}
			if inDenyPerm || !strings.Contains(line, "http.StatusForbidden") {
				continue
			}
			if !strings.Contains(line, "writeError(w, http.StatusForbidden,") && !strings.Contains(line, "http.Error(") {
				continue
			}
			ok := false
			for _, a := range allowed {
				if strings.Contains(line, a) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s:%d answers 403 without auditing; use h.denyPerm", f, i+1)
			}
		}
	}
}
