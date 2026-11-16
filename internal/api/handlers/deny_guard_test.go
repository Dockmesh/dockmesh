package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Handler-level permission refusals go through denyPerm or
// writeScopeDenied so they are audited. The listed lines answer 403 for
// something other than a missing permission (ownership, token
// restrictions) or audit the refusal themselves.
func TestForbiddenGoesThroughDenyPerm(t *testing.T) {
	allowed := []string{
		`"you can only change your own avatar"`,
		`"not your notification"`,
		`"a token can only create tokens restricted at least as far as itself"`,
		`"out of scope: none of your assignments allows this here"`,
		`"choose the host this stack is for`,
		`writeError(w, http.StatusForbidden, msg)`,
		`writeError(w, http.StatusForbidden,` + "\n",
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
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "writeError(w, http.StatusForbidden,") {
				continue
			}
			ok := false
			for _, a := range allowed {
				if strings.Contains(line+"\n", a) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s:%d answers 403 without auditing; use h.denyPerm", f, i+1)
			}
		}
	}
}
