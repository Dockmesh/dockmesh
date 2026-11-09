package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var verb = regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?\[([0-9]+)\]([a-zA-Z])`)

func placeholders(tmpl string) string {
	var out []string
	for _, m := range verb.FindAllStringSubmatch(tmpl, -1) {
		out = append(out, m[1]+m[2])
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestCatalogsMatch(t *testing.T) {
	for lang, cat := range catalogs {
		if lang == Default {
			continue
		}
		for k, v := range en {
			tr, ok := cat[k]
			if !ok {
				t.Errorf("%s: missing %s", lang, k)
				continue
			}
			if placeholders(v) != placeholders(tr) {
				t.Errorf("%s: %s placeholders %q, en has %q", lang, k, placeholders(tr), placeholders(v))
			}
		}
		for k := range cat {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: %s is not in the English catalog", lang, k)
			}
		}
	}
	unindexed := regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z]`)
	for k, v := range en {
		if unindexed.MatchString(strings.ReplaceAll(v, "%%", "")) {
			t.Errorf("en: %s uses a verb without an argument index", k)
		}
	}
}

func TestRender(t *testing.T) {
	cases := []struct {
		lang string
		text Text
		want string
	}{
		{"en", Msg("alert.container_restarts", "web", 3, "5m"), "Container web restarted 3 times in 5m"},
		{"de", Msg("alert.container_restarts", "web", 3, "5m"), "Container web wurde in 5m 3-mal neu gestartet"},
		{"de", Msg("alert.disk_full", "/", "dm-agent1", 91.6, "data"), "Datenträger / auf dm-agent1 ist zu 92% voll (data)"},
		{"en", Msg("alert.metric_fired", "CPU", "web", "cpu", 93.25, ">", 80.0), "CPU on web: cpu = 93.2 (threshold > 80.0)"},
		{"fr", Msg("alert.backup_failed", "nightly"), "Backup job nightly failed"},
		{"de", Msg("no.such.key"), "no.such.key"},
		{"de", Msg("alert.container_oom", Msg("alert.container_on_host", "web", "dm-agent1")), "Container web auf dm-agent1 wurde wegen Speichermangels beendet"},
		{"en", Msg("alert.container_oom", Msg("alert.container_on_host", "web", "dm-agent1")), "Container web on dm-agent1 was killed for running out of memory"},
	}
	for _, c := range cases {
		if got := c.text.In(c.lang); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.lang, c.text.Key, got, c.want)
		}
	}
}

func TestStoredRendersLikeTheOriginal(t *testing.T) {
	cases := []struct {
		name string
		text Text
	}{
		{"int verb", Msg("alert.container_restarts", "web", 3, "5m")},
		{"float verb with whole value", Msg("alert.metric_fired", "CPU", "web", "cpu", 93.0, ">", 80.0)},
		{"nested text", Msg("alert.container_oom", Msg("alert.container_on_host", "web", "dm-agent1"))},
		{"string list", Msg("notice.backup_missing.body", Bullets{"a", "b"})},
		{"text in list", Msg("notice.backup_repaired.body", Bullets{Msg("notice.backup_repaired.item", `"a"`, `"b"`)})},
		{"no args", Msg("notice.legacy_tokens.title")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, lang := range []string{"en", "de"} {
				if got, want := Stored(c.text.Encode(), lang, "fallback"), c.text.In(lang); got != want {
					t.Errorf("%s: %q, want %q", lang, got, want)
				}
			}
		})
	}
}

func TestStoredFallsBack(t *testing.T) {
	cases := []struct {
		name    string
		encoded string
	}{
		{"row without a key", ""},
		{"not json", "{"},
		{"key this version does not know", Msg("notice.removed_later", "x").Encode()},
		{"value json cannot hold", Msg("alert.metric_fired", "a", "b", "c", math.NaN(), ">", 1.0).Encode()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Stored(c.encoded, "de", "stored text"); got != "stored text" {
				t.Errorf("got %q", got)
			}
		})
	}
}

func TestBulletsRenderInLanguage(t *testing.T) {
	text := Msg("notice.backup_repaired.body", Bullets{Msg("notice.backup_repaired.item", `"a"`, `"b"`), Msg("notice.and_more", 2)})
	got := text.In("de")
	if !strings.Contains(got, "\n- \"a\" (Ziel \"b\")\n- und 2 weitere\n") {
		t.Errorf("got %q", got)
	}
}

// TestCallSitesMatchCatalog parses the server's sources: every key passed
// to i18n.Msg must exist with exactly the arguments its template uses, and
// every notice text must be used.
func TestCallSitesMatchCatalog(t *testing.T) {
	used := map[string]bool{}
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Msg" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "i18n" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					t.Errorf("%s: i18n.Msg key is not a literal", path)
					return true
				}
				key, _ := strconv.Unquote(lit.Value)
				used[key] = true
				tmpl, ok := en[key]
				if !ok {
					t.Errorf("%s: %s is not in the catalog", path, key)
					return true
				}
				if want := maxArgIndex(tmpl); len(call.Args)-1 != want {
					t.Errorf("%s: %s gets %d arguments, its template uses %d", path, key, len(call.Args)-1, want)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for k := range en {
		if strings.HasPrefix(k, "notice.") && !used[k] {
			t.Errorf("%s is not used", k)
		}
	}
}

func maxArgIndex(tmpl string) int {
	highest := 0
	for _, m := range verb.FindAllStringSubmatch(tmpl, -1) {
		if n, _ := strconv.Atoi(m[1]); n > highest {
			highest = n
		}
	}
	return highest
}
