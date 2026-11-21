package swarmconvert

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Diff names the fields in which the spec a deploy writes differs from
// the spec a service runs with, as dotted paths ("TaskTemplate.ContainerSpec.User").
// netNames maps network ids to names, as the converter names networks.
// What every deploy sets anew is left out: the image digest, the
// platforms, the spec hash and the ids of secrets and configs.
func Diff(running, deploy swarm.ServiceSpec, netNames map[string]string) []string {
	a, b := diffForm(running, netNames), diffForm(deploy, netNames)
	var out []string
	walkDiff("", a, b, &out)
	slices.Sort(out)
	return out
}

func diffForm(spec swarm.ServiceSpec, netNames map[string]string) any {
	spec.Labels = maps.Clone(spec.Labels)
	delete(spec.Labels, agents.LabelSpecHash)
	t := spec.TaskTemplate
	if c := t.ContainerSpec; c != nil {
		cc := *c
		if i := strings.Index(cc.Image, "@sha256:"); i > 0 {
			cc.Image = cc.Image[:i]
		}
		if cc.Isolation == "default" {
			cc.Isolation = ""
		}
		cc.Secrets = slices.Clone(cc.Secrets)
		for i, r := range cc.Secrets {
			if r != nil {
				x := *r
				x.SecretID = ""
				cc.Secrets[i] = &x
			}
		}
		cc.Configs = slices.Clone(cc.Configs)
		for i, r := range cc.Configs {
			if r != nil {
				x := *r
				x.ConfigID = ""
				cc.Configs[i] = &x
			}
		}
		t.ContainerSpec = &cc
	}
	if t.Runtime == swarm.RuntimeContainer {
		t.Runtime = ""
	}
	if p := t.Placement; p != nil {
		pp := *p
		pp.Platforms = nil
		t.Placement = &pp
	}
	t.ForceUpdate = 0
	t.Networks = slices.Clone(t.Networks)
	for i, n := range t.Networks {
		if name, ok := netNames[n.Target]; ok {
			t.Networks[i].Target = name
		}
	}
	slices.SortFunc(t.Networks, func(x, y swarm.NetworkAttachmentConfig) int { return strings.Compare(x.Target, y.Target) })
	spec.TaskTemplate = t
	if e := spec.EndpointSpec; e != nil {
		ee := *e
		if ee.Mode == swarm.ResolutionModeVIP {
			ee.Mode = ""
		}
		ee.Ports = slices.Clone(ee.Ports)
		for i := range ee.Ports {
			if ee.Ports[i].Protocol == "" {
				ee.Ports[i].Protocol = "tcp"
			}
		}
		spec.EndpointSpec = &ee
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return pruneZero(v)
}

// pruneZero drops null, zero and empty values, so a field the daemon
// fills with its default and one left unset compare equal.
func pruneZero(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if p := pruneZero(val); p == nil {
				delete(x, k)
			} else {
				x[k] = p
			}
		}
		if len(x) == 0 {
			return nil
		}
		return x
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			p := pruneZero(val)
			if p == nil {
				p = map[string]any{}
			}
			out = append(out, p)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if x == "" {
			return nil
		}
	case bool:
		if !x {
			return nil
		}
	case float64:
		if x == 0 {
			return nil
		}
	}
	return v
}

func walkDiff(path string, a, b any, out *[]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkDiff(p, am[k], bm[k], out)
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, path)
	}
}
