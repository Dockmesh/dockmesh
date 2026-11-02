package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every fixed message needs a code so the UI can show it translated.
func TestErrorMessagesHaveCodes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fs := token.NewFileSet()
		af, err := parser.ParseFile(fs, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := c.Fun.(*ast.Ident)
			if !ok || id.Name != "writeError" || len(c.Args) != 3 {
				return true
			}
			bl, ok := c.Args[2].(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			msg, err := strconv.Unquote(bl.Value)
			if err != nil {
				t.Fatal(err)
			}
			used[msg] = true
			if _, ok := errorCodes[msg]; !ok {
				t.Errorf("%s: writeError message %q has no entry in errorCodes", fs.Position(c.Pos()), msg)
			}
			return true
		})
	}
	for msg := range errorCodes {
		if !used[msg] {
			t.Errorf("errorCodes entry %q is not used by any writeError call", msg)
		}
	}
}

func TestErrorCodesAreUniqueIdentifiers(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	seen := map[string]string{}
	for msg, code := range errorCodes {
		if !valid.MatchString(code) {
			t.Errorf("code %q for %q is not a lower-case identifier", code, msg)
		}
		if strings.HasPrefix(code, "http_") {
			t.Errorf("code %q for %q uses the prefix of the generic codes", code, msg)
		}
		if other, dup := seen[code]; dup {
			t.Errorf("code %q used for %q and %q", code, msg, other)
		}
		seen[code] = msg
	}
}

func TestWriteErrorCode(t *testing.T) {
	cases := []struct {
		status int
		msg    string
		want   string
	}{
		{http.StatusBadRequest, "invalid body", "invalid_body"},
		{http.StatusBadRequest, "port 99999: out of range", "http_bad_request"},
		{http.StatusNotFound, "stack foo: no such file", "http_not_found"},
		{http.StatusInternalServerError, "disk full", "http_internal"},
		{http.StatusTeapot, "odd", "http_error"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeError(rec, c.status, c.msg)
		body := rec.Body.String()
		if !strings.Contains(body, `"code":"`+c.want+`"`) || !strings.Contains(body, `"error":`) {
			t.Errorf("writeError(%d, %q) body %s, want code %q", c.status, c.msg, body, c.want)
		}
	}
}
