package compose

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"gopkg.in/yaml.v3"
)

// ErrLabelsNeedAgent refuses a compose file whose labels the server can't
// see without reading files on the agent's host.
var ErrLabelsNeedAgent = errors.New("the agent on this host doesn't check labels set through other files or variables yet; upgrade the agent from the Hosts page")

// CheckReservedLabelsText is CheckReservedLabels for a compose file the
// server only forwards to an agent that doesn't check labels itself. It
// reads nothing but the text: loading the project would open every file
// include, extends and env_file name, on the server. Whatever it can't
// decide from the text alone is refused.
func CheckReservedLabelsText(stackName, composeYAML string) error {
	var doc struct {
		Include  any `yaml:"include"`
		Services map[string]struct {
			Labels    any `yaml:"labels"`
			LabelFile any `yaml:"label_file"`
			Extends   any `yaml:"extends"`
		} `yaml:"services"`
		Networks map[string]*rawResource `yaml:"networks"`
		Volumes  map[string]*rawResource `yaml:"volumes"`
	}
	dec := yaml.NewDecoder(strings.NewReader(composeYAML))
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse compose file: %w", err)
	}
	// Compose merges every document of the file; this check reads one.
	var more any
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return fmt.Errorf("several YAML documents: %w", ErrLabelsNeedAgent)
	}
	if doc.Include != nil {
		return fmt.Errorf("include: %w", ErrLabelsNeedAgent)
	}
	project := loader.NormalizeProjectName(stackName)
	base := map[string]string{LabelProject: project, LabelManaged: "true"}
	extends := false
	for _, svc := range doc.Services {
		extends = extends || svc.Extends != nil
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Services)) {
		svc := doc.Services[name]
		if svc.LabelFile != nil {
			return fmt.Errorf("service %q: label_file: %w", name, ErrLabelsNeedAgent)
		}
		if !sameFileExtends(svc.Extends) {
			return fmt.Errorf("service %q: extends from another file: %w", name, ErrLabelsNeedAgent)
		}
		tracking := maps.Clone(base)
		tracking[LabelService] = name
		if extends {
			// An extended service passes its labels on, and with them a
			// service label that is right only for itself.
			tracking[LabelService] = ""
		}
		if err := checkRawLabels("service", name, tracking, svc.Labels); err != nil {
			return err
		}
	}
	for kind, set := range map[string]map[string]*rawResource{"network": doc.Networks, "volume": doc.Volumes} {
		for _, name := range slices.Sorted(maps.Keys(set)) {
			r := set[name]
			if r == nil || isExternal(r.External) {
				continue
			}
			if err := checkRawLabels(kind, name, base, r.Labels); err != nil {
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

type rawResource struct {
	External any `yaml:"external"`
	Labels   any `yaml:"labels"`
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

// checkRawLabels applies withTrackingLabels to labels as written. A key
// or value with a variable in it is refused: its final text depends on
// interpolation the server doesn't run.
func checkRawLabels(kind, name string, tracking map[string]string, raw any) error {
	labels := map[string]string{}
	switch x := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		for k, v := range x {
			labels[k] = fmt.Sprint(v)
		}
	case []any:
		for _, e := range x {
			k, v, _ := strings.Cut(fmt.Sprint(e), "=")
			labels[k] = v
		}
	default:
		return fmt.Errorf("%s %q: labels must be a map or a list", kind, name)
	}
	for k, v := range labels {
		if strings.Contains(k, "$") || (strings.Contains(v, "$") && isTrackedOrReserved(k, tracking)) {
			return fmt.Errorf("%s %q: label %q uses a variable: %w", kind, name, k, ErrLabelsNeedAgent)
		}
	}
	_, err := withTrackingLabels(kind, name, tracking, labels)
	return err
}

func isTrackedOrReserved(k string, tracking map[string]string) bool {
	_, tracked := tracking[k]
	return tracked || strings.HasPrefix(k, reservedLabelPrefix)
}
