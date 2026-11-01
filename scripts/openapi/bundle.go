package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const rootFile = "root.yaml"

const header = "Generated from api/openapi/ by `go run ./scripts/openapi`; edit those files, not this one."

// Bundle merges root.yaml and every other *.yaml file in dir into one
// spec. Area files may only hold paths and components; their entries are
// appended in file-name order and a key defined twice is an error.
func Bundle(dir string) ([]byte, error) {
	root, err := readMapping(filepath.Join(dir, rootFile))
	if err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	seen := map[string]string{}
	for _, name := range names {
		base := filepath.Base(name)
		if base == rootFile {
			continue
		}
		area, err := readMapping(name)
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(area.Content); i += 2 {
			key, val := area.Content[i], area.Content[i+1]
			switch key.Value {
			case "paths":
				if err := merge(root, []string{"paths"}, val, base, seen); err != nil {
					return nil, err
				}
			case "components":
				for j := 0; j+1 < len(val.Content); j += 2 {
					kind := val.Content[j].Value
					if err := merge(root, []string{"components", kind}, val.Content[j+1], base, seen); err != nil {
						return nil, err
					}
				}
			default:
				return nil, fmt.Errorf("%s: top-level key %q belongs in %s; area files hold paths and components only", base, key.Value, rootFile)
			}
		}
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: header, Content: []*yaml.Node{root}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// merge appends the pairs of src to the mapping at path below root,
// creating it at the end of its parent when root.yaml doesn't declare it.
func merge(root *yaml.Node, path []string, src *yaml.Node, file string, seen map[string]string) error {
	if src.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: %s is not a mapping", file, strings.Join(path, "."))
	}
	dst := root
	for _, k := range path {
		next := lookup(dst, k)
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			dst.Content = append(dst.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, next)
		}
		dst = next
	}
	// root.yaml writes empty sections as {} to fix their position.
	dst.Style &^= yaml.FlowStyle
	prefix := strings.Join(path, ".") + "."
	for i := 0; i+1 < len(src.Content); i += 2 {
		id := prefix + src.Content[i].Value
		if prev, ok := seen[id]; ok {
			return fmt.Errorf("%s: %s is already defined in %s", file, id, prev)
		}
		if lookup(dst, src.Content[i].Value) != nil {
			return fmt.Errorf("%s: %s is already defined in %s", file, id, rootFile)
		}
		seen[id] = file
		dst.Content = append(dst.Content, src.Content[i], src.Content[i+1])
	}
	return nil
}

func lookup(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func readMapping(path string) (*yaml.Node, error) {
	b, err := os.ReadFile(path) //nolint:gosec // repository file named by the caller
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a YAML mapping", filepath.Base(path))
	}
	return doc.Content[0], nil
}
