package swarmgolden

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/swarm"
)

// Diff is one difference between a recorded spec and the converter's.
type Diff struct {
	Path string // <service>:<key>/<key>/…, array elements as [i]
	Want string // JSON of the recorded value, "" when missing
	Got  string // JSON of the converter's value, "" when missing
}

func (d Diff) String() string {
	return fmt.Sprintf("%s: want %s, got %s", d.Path, orMissing(d.Want), orMissing(d.Got))
}

func orMissing(s string) string {
	if s == "" {
		return "(missing)"
	}
	return s
}

// CompareServices compares specs by service name. ignored holds paths of
// the form "<service>:<key>/<key>" or "*:<key>/<key>"; a path drops that
// key with everything below it on both sides. Keys are the JSON names of
// the API types, so label names keep their dots. null, {} and [] count as
// missing, which matches how the API types omit empty values; zero
// scalars are compared, so Replicas 0 and an unset Replicas differ.
func CompareServices(want, got []swarm.ServiceSpec, ignored []string) ([]Diff, error) {
	w, err := byName(want)
	if err != nil {
		return nil, err
	}
	g, err := byName(got)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for n := range w {
		names[n] = true
	}
	for n := range g {
		names[n] = true
	}
	var out []Diff
	for _, n := range sortedKeys(names) {
		wv, wok := w[n]
		gv, gok := g[n]
		switch {
		case !wok:
			out = append(out, Diff{Path: n + ":", Got: "service"})
			continue
		case !gok:
			out = append(out, Diff{Path: n + ":", Want: "service"})
			continue
		}
		for _, p := range ignored {
			svc, key, _ := strings.Cut(p, ":")
			if svc == "*" || svc == n {
				drop(wv, strings.Split(key, "/"))
				drop(gv, strings.Split(key, "/"))
			}
		}
		out = append(out, diff(n+":", prune(wv), prune(gv))...)
	}
	return out, nil
}

func byName(specs []swarm.ServiceSpec) (map[string]any, error) {
	out := map[string]any{}
	for _, s := range specs {
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if _, dup := out[s.Name]; dup {
			return nil, fmt.Errorf("service %s twice", s.Name)
		}
		out[s.Name] = v
	}
	return out, nil
}

func drop(v any, path []string) {
	m, ok := v.(map[string]any)
	if !ok || len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(m, path[0])
		return
	}
	switch child := m[path[0]].(type) {
	case map[string]any:
		drop(child, path[1:])
	case []any:
		for _, el := range child {
			drop(el, path[1:])
		}
	}
}

// prune removes null, {} and [] recursively; nil means missing.
func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, el := range t {
			if p := prune(el); p == nil {
				delete(t, k)
			} else {
				t[k] = p
			}
		}
		if len(t) == 0 {
			return nil
		}
		return t
	case []any:
		out := make([]any, 0, len(t))
		for _, el := range t {
			if p := prune(el); p != nil {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

func diff(path string, want, got any) []Diff {
	wm, wIsMap := want.(map[string]any)
	gm, gIsMap := got.(map[string]any)
	if wIsMap && got == nil {
		gm, gIsMap = map[string]any{}, true
	}
	if gIsMap && want == nil {
		wm, wIsMap = map[string]any{}, true
	}
	if wIsMap && gIsMap {
		keys := map[string]bool{}
		for k := range wm {
			keys[k] = true
		}
		for k := range gm {
			keys[k] = true
		}
		var out []Diff
		for _, k := range sortedKeys(keys) {
			out = append(out, diff(join(path, k), wm[k], gm[k])...)
		}
		return out
	}
	wa, wIsArr := want.([]any)
	ga, gIsArr := got.([]any)
	if wIsArr && gIsArr && len(wa) == len(ga) {
		var out []Diff
		for i := range wa {
			out = append(out, diff(fmt.Sprintf("%s[%d]", path, i), wa[i], ga[i])...)
		}
		return out
	}
	if reflect.DeepEqual(want, got) {
		return nil
	}
	return []Diff{{Path: path, Want: jsonOf(want), Got: jsonOf(got)}}
}

func join(path, key string) string {
	if strings.HasSuffix(path, ":") {
		return path + key
	}
	return path + "/" + key
}

func jsonOf(v any) string {
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
