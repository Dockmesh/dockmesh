package audit

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

// dynamicActions lists the values of actions built at runtime, keyed by
// the literal prefix the call site concatenates.
var dynamicActions = map[string][]string{
	"proxy.route_": {"proxy.route_enabled", "proxy.route_disabled"},
}

// forwarders pass an action parameter through to the audit log; the
// index is the position of the action argument.
var forwarders = map[string]int{
	"audit":              1,
	"auditDetached":      1,
	"containerAction":    3,
	"writeVersionSwitch": 2,
}

// TestEveryEmittedActionHasCategory parses the server and agent sources,
// collects every action passed to the audit log and requires a category
// for each, and no category for an action nothing emits.
func TestEveryEmittedActionHasCategory(t *testing.T) {
	fset := token.NewFileSet()
	consts := map[string]string{}
	var files []*ast.File
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			if f.Name.Name == "audit" && filepath.Base(filepath.Dir(path)) == "audit" {
				collectActionConsts(f, consts)
			}
			files = append(files, f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	emitted := map[string]bool{}
	for _, v := range consts {
		emitted[v] = true
	}
	for _, f := range files {
		inAudit := f.Name.Name == "audit"
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			params := map[string]bool{}
			if _, fwd := forwarders[fn.Name.Name]; fwd {
				for _, field := range fn.Type.Params.List {
					for _, name := range field.Names {
						params[name.Name] = true
					}
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				arg, ok := actionArg(call)
				if !ok {
					return true
				}
				values, ok := resolveAction(arg, consts, inAudit, params)
				if !ok {
					t.Errorf("%s: cannot resolve the audit action of this call; add it to dynamicActions", fset.Position(call.Pos()))
					return true
				}
				for _, v := range values {
					emitted[v] = true
				}
				return true
			})
			return false
		})
	}

	if len(emitted) < 50 {
		t.Fatalf("found only %d actions; the source walk is broken", len(emitted))
	}
	for a := range emitted {
		if _, ok := categories[a]; !ok {
			t.Errorf("action %q is emitted but has no category in categories", a)
		}
	}
	for a, c := range categories {
		if !emitted[a] {
			t.Errorf("action %q has a category but nothing emits it", a)
		}
		if !ValidCategory(c) {
			t.Errorf("action %q has unknown category %q", a, c)
		}
	}
}

func collectActionConsts(f *ast.File, consts map[string]string) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Action") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if v, err := strconv.Unquote(lit.Value); err == nil {
						consts[name.Name] = v
					}
				}
			}
		}
	}
}

// actionArg returns the action argument of an audit call: h.audit,
// h.auditDetached, h.containerAction, or Write(ctx, user, action, target,
// details) on an audit service.
func actionArg(call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if idx, ok := forwarders[sel.Sel.Name]; ok && len(call.Args) > idx {
		return call.Args[idx], true
	}
	if sel.Sel.Name == "Write" && len(call.Args) == 5 && strings.Contains(strings.ToLower(exprString(sel.X)), "audit") {
		return call.Args[2], true
	}
	return nil, false
}

func resolveAction(e ast.Expr, consts map[string]string, inAudit bool, params map[string]bool) ([]string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		v, err := strconv.Unquote(x.Value)
		return []string{v}, err == nil
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "audit" {
			v, ok := consts[x.Sel.Name]
			return []string{v}, ok
		}
	case *ast.Ident:
		if params[x.Name] {
			return nil, true
		}
		if inAudit {
			v, ok := consts[x.Name]
			return []string{v}, ok
		}
	case *ast.BinaryExpr:
		if lit, ok := x.X.(*ast.BasicLit); ok && x.Op == token.ADD {
			prefix, err := strconv.Unquote(lit.Value)
			if vs, ok := dynamicActions[prefix]; ok && err == nil {
				return vs, true
			}
		}
	}
	return nil, false
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	}
	return ""
}

func TestCategory(t *testing.T) {
	tests := []struct{ action, want string }{
		{ActionContainerExecStart, CategoryAccess},
		{ActionContainerWriteFile, CategoryChanges},
		{ActionScopeDenied, CategoryAuth},
		{ActionGenesis, CategorySystem},
		{"something.from_a_newer_version", CategorySystem},
	}
	for _, tt := range tests {
		if got := Category(tt.action); got != tt.want {
			t.Errorf("Category(%q) = %q, want %q", tt.action, got, tt.want)
		}
	}
}

func TestListFilteredByCategory(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	s := NewService(database, filepath.Join(t.TempDir(), "genesis"))
	ctx := context.Background()
	for _, a := range []string{ActionStackDeploy, ActionContainerReadFile, ActionLogin, "something.unknown"} {
		s.Write(ctx, "", a, "t", nil)
	}
	tests := []struct {
		category string
		want     []string
	}{
		{"", []string{"something.unknown", ActionLogin, ActionContainerReadFile, ActionStackDeploy}},
		{CategoryChanges, []string{ActionStackDeploy}},
		{CategoryAccess, []string{ActionContainerReadFile}},
		{CategoryAuth, []string{ActionLogin}},
		{CategorySystem, []string{"something.unknown"}},
	}
	for _, tt := range tests {
		entries, err := s.ListFiltered(ctx, ListFilter{Category: tt.category})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.Action)
			if e.Category != Category(e.Action) {
				t.Errorf("%s: category %q, want %q", e.Action, e.Category, Category(e.Action))
			}
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("category %q: got %v, want %v", tt.category, got, tt.want)
		}
	}
}
