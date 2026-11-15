package compose

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"gopkg.in/yaml.v3"
)

// ErrLabelsNeedAgent refuses a compose file whose labels the server can't
// see without reading files on the agent's host.
var ErrLabelsNeedAgent = errors.New("this compose file takes labels from other files (include, extends from another file, label_file), which the agent on this host doesn't check yet; upgrade the agent from the Hosts page")

// CheckReservedLabelsText is CheckReservedLabels for a compose file the
// server only forwards to an agent that doesn't check labels itself. It
// reads nothing but the text: loading the project would open every file
// include, extends and env_file name, on the server. Whatever it can't
// decide from the text alone is refused.
func CheckReservedLabelsText(stackName, composeYAML string) error {
	var doc struct {
		Include  any `yaml:"include"`
		Services map[string]struct {
			Labels     any `yaml:"labels"`
			LabelFile  any `yaml:"label_file"`
			LabelsFile any `yaml:"labels_file"`
			Extends    any `yaml:"extends"`
		} `yaml:"services"`
		Networks map[string]*rawResource `yaml:"networks"`
		Volumes  map[string]*rawResource `yaml:"volumes"`
	}
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return fmt.Errorf("parse compose file: %w", err)
	}
	if doc.Include != nil {
		return ErrLabelsNeedAgent
	}
	project := loader.NormalizeProjectName(stackName)
	base := map[string]string{LabelProject: project, LabelManaged: "true"}
	for _, name := range slices.Sorted(maps.Keys(doc.Services)) {
		svc := doc.Services[name]
		if svc.LabelFile != nil || svc.LabelsFile != nil {
			return ErrLabelsNeedAgent
		}
		if ext, ok := svc.Extends.(map[string]any); ok && ext["file"] != nil {
			return ErrLabelsNeedAgent
		}
		tracking := maps.Clone(base)
		tracking[LabelService] = name
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
			return fmt.Errorf("%s %q: label %q uses a variable, which the agent on this host doesn't check yet; upgrade the agent from the Hosts page", kind, name, k)
		}
	}
	_, err := withTrackingLabels(kind, name, tracking, labels)
	return err
}

func isTrackedOrReserved(k string, tracking map[string]string) bool {
	_, tracked := tracking[k]
	return tracked || strings.HasPrefix(k, reservedLabelPrefix)
}
