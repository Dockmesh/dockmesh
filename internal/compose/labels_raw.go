package compose

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrLabelsNeedAgent refuses a compose file whose labels the server can't
// see without reading files on the agent's host.
var ErrLabelsNeedAgent = errors.New("the agent on this host doesn't check labels set through other files or variables yet; upgrade the agent from the Hosts page")

// ErrEnvFileNeedAgent refuses an env_file an older agent would read
// without checking that it stays inside the stack folder.
var ErrEnvFileNeedAgent = errors.New("the agent on this host doesn't check env_file paths yet; use a relative path inside the stack folder, without .. or variables, or upgrade the agent from the Hosts page")

// textKeys are the keys the text check reads. compose-go matches keys
// case-insensitively, so a spelling with upper-case letters would reach
// the agent's loader past this check.
var textKeys = map[string]bool{
	"include": true, "services": true, "networks": true, "volumes": true,
	"labels": true, "label_file": true, "extends": true, "env_file": true,
	"pre_start": true, "external": true,
}

// CheckReservedLabelsText is CheckReservedLabels for a compose file the
// server only forwards to an agent that doesn't check labels or file
// references itself. It reads nothing but the text: loading the project
// would open every file include, extends and env_file name, on the
// server. Whatever it can't decide from the text alone is refused, and
// env_file paths that could leave the stack folder are refused as well.
func CheckReservedLabelsText(composeYAML string) error {
	var doc struct {
		Include  any `yaml:"include"`
		Services map[string]struct {
			Labels    yaml.Node `yaml:"labels"`
			LabelFile any       `yaml:"label_file"`
			Extends   any       `yaml:"extends"`
		} `yaml:"services"`
		Networks map[string]*rawResource `yaml:"networks"`
		Volumes  map[string]*rawResource `yaml:"volumes"`
	}
	dec := yaml.NewDecoder(strings.NewReader(composeYAML))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse compose file: %w", err)
	}
	if d := unsupportedNode(&root); d != "" {
		return fmt.Errorf("%s: %w", d, ErrLabelsNeedAgent)
	}
	var plain any
	if err := root.Decode(&plain); err != nil {
		return fmt.Errorf("parse compose file: %w", err)
	}
	if err := root.Decode(&doc); err != nil {
		return fmt.Errorf("parse compose file: %w", err)
	}
	// Compose merges every document of the file; this check reads one.
	var more any
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return fmt.Errorf("several YAML documents: %w", ErrLabelsNeedAgent)
	}
	if err := checkTextKeys(plain); err != nil {
		return err
	}
	if doc.Include != nil {
		return fmt.Errorf("include: %w", ErrLabelsNeedAgent)
	}
	if err := checkTextFileRefs(plain); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Services)) {
		svc := doc.Services[name]
		if svc.LabelFile != nil {
			return fmt.Errorf("service %q: label_file: %w", name, ErrLabelsNeedAgent)
		}
		if !sameFileExtends(svc.Extends) {
			return fmt.Errorf("service %q: extends from another file: %w", name, ErrLabelsNeedAgent)
		}
		if err := checkRawLabels("service", name, svc.Labels); err != nil {
			return err
		}
	}
	for kind, set := range map[string]map[string]*rawResource{"network": doc.Networks, "volume": doc.Volumes} {
		for _, name := range slices.Sorted(maps.Keys(set)) {
			r := set[name]
			if r == nil || isExternal(r.External) {
				continue
			}
			if err := checkRawLabels(kind, name, r.Labels); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameFileExtends accepts no extends, or one naming only a service of this
// file.
func sameFileExtends(v any) bool {
	switch x := v.(type) {
	case nil, string:
		return true
	case map[string]any:
		for k := range x {
			if k != "service" {
				return false
			}
		}
		return true
	}
	return false
}

// unsupportedNode describes the first node of n that two YAML parsers
// may read differently: a tag that isn't a standard YAML one (compose's
// !reset and !override change what a merge leaves behind), or a plain
// scalar ending in ":" (in a flow map, "{key:}" is a key with an empty
// value to compose but a key ending in ":" to this parser). Aliases
// aren't followed: their anchor is part of the tree itself.
func unsupportedNode(n *yaml.Node) string {
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		return "YAML tag " + n.Tag
	}
	if n.Kind == yaml.ScalarNode && n.Style == 0 && strings.HasSuffix(n.Value, ":") {
		return fmt.Sprintf("plain value %q", n.Value)
	}
	for _, c := range n.Content {
		if d := unsupportedNode(c); d != "" {
			return d
		}
	}
	return ""
}

type rawResource struct {
	External any       `yaml:"external"`
	Labels   yaml.Node `yaml:"labels"`
}

func isExternal(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case map[string]any:
		return true
	}
	return false
}

// checkRawLabels refuses every label under com.docker.compose. and
// dockmesh., whatever its value: compose may turn a value into other
// text (an unquoted 1e2 becomes 100), so a value that matches here may
// not match on the agent. A key with a variable in it is refused too.
func checkRawLabels(kind, name string, raw yaml.Node) error {
	labels := map[string]string{}
	switch raw.Kind {
	case 0:
		return nil
	case yaml.MappingNode, yaml.AliasNode:
		if err := raw.Decode(&labels); err != nil {
			return fmt.Errorf("%s %q: labels: %w", kind, name, err)
		}
	case yaml.SequenceNode:
		var list []string
		if err := raw.Decode(&list); err != nil {
			return fmt.Errorf("%s %q: labels: %w", kind, name, err)
		}
		for _, e := range list {
			k, v, _ := strings.Cut(e, "=")
			labels[k] = v
		}
	default:
		return fmt.Errorf("%s %q: labels must be a map or a list", kind, name)
	}
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		switch {
		case strings.Contains(k, "$"):
			return fmt.Errorf("%s %q: label %q uses a variable: %w", kind, name, k, ErrLabelsNeedAgent)
		case strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, reservedLabelPrefix):
			return fmt.Errorf("%s %q: label %q is reserved for Dockmesh; remove it from the compose file", kind, name, k)
		}
	}
	return nil
}

// asMap returns a decoded YAML mapping with its keys as strings, or nil.
func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[fmt.Sprint(k)] = x
		}
		return out
	}
	return nil
}

func checkFoldedKeys(where string, m map[string]any) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if l := strings.ToLower(k); l != k && textKeys[l] {
			why := ErrLabelsNeedAgent
			if l == "env_file" {
				why = ErrEnvFileNeedAgent
			}
			return fmt.Errorf("%skey %q: write it as %q: %w", where, k, l, why)
		}
	}
	return nil
}

func checkTextKeys(plain any) error {
	top := asMap(plain)
	if err := checkFoldedKeys("", top); err != nil {
		return err
	}
	services := asMap(top["services"])
	for _, name := range slices.Sorted(maps.Keys(services)) {
		svc := asMap(services[name])
		where := fmt.Sprintf("service %q: ", name)
		if err := checkFoldedKeys(where, svc); err != nil {
			return err
		}
		hooks, _ := svc["pre_start"].([]any)
		for _, h := range hooks {
			if err := checkFoldedKeys(where+"pre_start: ", asMap(h)); err != nil {
				return err
			}
		}
	}
	for _, kind := range []string{"networks", "volumes"} {
		set := asMap(top[kind])
		for _, name := range slices.Sorted(maps.Keys(set)) {
			if err := checkFoldedKeys(fmt.Sprintf("%s %q: ", kind, name), asMap(set[name])); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkTextFileRefs refuses the env_file entries of services and their
// pre_start hooks that an older agent would resolve outside the stack
// folder, and label_file on hooks. Only plain relative paths are
// accepted: compose-go turns other scalars into paths too, and
// interpolation or ~ would only be resolved on the agent.
func checkTextFileRefs(plain any) error {
	services := asMap(asMap(plain)["services"])
	for _, name := range slices.Sorted(maps.Keys(services)) {
		svc := asMap(services[name])
		where := fmt.Sprintf("service %q", name)
		if err := checkTextEnvFile(where, svc); err != nil {
			return err
		}
		hooks, ok := svc["pre_start"]
		if !ok {
			continue
		}
		list, ok := hooks.([]any)
		if !ok {
			return fmt.Errorf("%s: pre_start must be a list: %w", where, ErrEnvFileNeedAgent)
		}
		for i, h := range list {
			hook := asMap(h)
			hwhere := fmt.Sprintf("%s: pre_start[%d]", where, i)
			if _, ok := hook["label_file"]; ok {
				return fmt.Errorf("%s: label_file: %w", hwhere, ErrLabelsNeedAgent)
			}
			if err := checkTextEnvFile(hwhere, hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkTextEnvFile(where string, spec map[string]any) error {
	v, ok := spec["env_file"]
	if !ok {
		return nil
	}
	var entries []any
	switch x := v.(type) {
	case string:
		entries = []any{x}
	case []any:
		entries = x
	default:
		return fmt.Errorf("%s: env_file must be a path or a list: %w", where, ErrEnvFileNeedAgent)
	}
	for _, e := range entries {
		p, ok := e.(string)
		if m, isMap := e.(map[string]any); isMap {
			for k := range m {
				switch k {
				case "path", "required", "format":
				default:
					return fmt.Errorf("%s: env_file: unsupported key %q: %w", where, k, ErrEnvFileNeedAgent)
				}
			}
			p, ok = m["path"].(string)
		}
		if !ok {
			return fmt.Errorf("%s: env_file entries must be a path or a mapping with a path: %w", where, ErrEnvFileNeedAgent)
		}
		if reason := envFilePathProblem(p); reason != "" {
			return fmt.Errorf("%s: env_file %q %s: %w", where, p, reason, ErrEnvFileNeedAgent)
		}
	}
	return nil
}

func envFilePathProblem(p string) string {
	switch {
	case p == "":
		return "is empty"
	case strings.Contains(p, "$"):
		return "uses a variable"
	case strings.HasPrefix(p, "~"):
		return "starts with ~"
	case strings.HasPrefix(p, "/"):
		return "is absolute"
	case slices.Contains(strings.Split(p, "/"), ".."):
		return "contains .."
	}
	return ""
}
