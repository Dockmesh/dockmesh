package compose

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
)

// reservedLabelPrefix marks Dockmesh's own resources (helpers, the proxy);
// the server treats any container carrying such a label as its own.
const reservedLabelPrefix = "dockmesh."

// withTrackingLabels returns the user's labels plus Dockmesh's tracking
// labels. A user label may repeat a tracking label with the same value
// (copied from `docker inspect`), but not change it: the project label
// decides which stack a container counts for, and with it who may act on
// it.
func withTrackingLabels(kind, name string, tracking map[string]string, user composetypes.Labels) (map[string]string, error) {
	out := make(map[string]string, len(tracking)+len(user))
	for _, k := range slices.Sorted(maps.Keys(user)) {
		v := user[k]
		want, tracked := tracking[k]
		if (tracked && v != want) || (!tracked && strings.HasPrefix(k, reservedLabelPrefix)) {
			return nil, fmt.Errorf("%s %q: label %q is reserved for Dockmesh; remove it from the compose file", kind, name, k)
		}
		out[k] = v
	}
	for k, v := range tracking {
		out[k] = v
	}
	return out, nil
}

func trackingLabels(proj *composetypes.Project) map[string]string {
	return map[string]string{LabelProject: proj.Name, LabelManaged: "true"}
}

func serviceTrackingLabels(proj *composetypes.Project, svc string) map[string]string {
	l := trackingLabels(proj)
	l[LabelService] = svc
	return l
}

// CheckReservedLabels refuses a project whose services, networks or
// volumes set one of Dockmesh's tracking labels, before anything of it
// is created. Labels of external networks and volumes are never applied.
func CheckReservedLabels(proj *composetypes.Project) error {
	for _, name := range slices.Sorted(maps.Keys(proj.Services)) {
		if _, err := withTrackingLabels("service", name, serviceTrackingLabels(proj, name), proj.Services[name].Labels); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Networks)) {
		if bool(proj.Networks[name].External) {
			continue
		}
		if _, err := withTrackingLabels("network", name, trackingLabels(proj), proj.Networks[name].Labels); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(proj.Volumes)) {
		if bool(proj.Volumes[name].External) {
			continue
		}
		if _, err := withTrackingLabels("volume", name, trackingLabels(proj), proj.Volumes[name].Labels); err != nil {
			return err
		}
	}
	return nil
}
