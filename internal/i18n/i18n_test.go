package i18n

import (
	"regexp"
	"sort"
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
	}
	for _, c := range cases {
		if got := c.text.In(c.lang); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.lang, c.text.Key, got, c.want)
		}
	}
}
