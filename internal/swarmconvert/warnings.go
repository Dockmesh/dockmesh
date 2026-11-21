package swarmconvert

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// pinned reports whether a placement constraint ties svc to one node, so
// node-local data stays where the task runs.
func pinned(svc types.ServiceConfig) bool {
	if svc.Deploy == nil {
		return false
	}
	for _, c := range svc.Deploy.Placement.Constraints {
		c = strings.ReplaceAll(c, " ", "")
		if strings.HasPrefix(c, "node.hostname==") || strings.HasPrefix(c, "node.id==") {
			return true
		}
	}
	return false
}

// serviceWarnings spells out what the mounts of svc mean on a Swarm,
// where each task may run on a different node.
func serviceWarnings(svc types.ServiceConfig, project *types.Project) []Warning {
	var out []Warning
	add := func(key, msg string) { out = append(out, Warning{Service: svc.Name, Key: key, Message: msg}) }
	onOneNode := pinned(svc)
	for _, v := range svc.Volumes {
		key := "volumes " + v.Target
		switch v.Type {
		case types.VolumeTypeVolume, "":
			if v.Source == "" {
				add(key, "Anonymous volume: every task gets a new, empty one.")
				continue
			}
			if onOneNode {
				continue
			}
			vol := project.Volumes[v.Source]
			switch {
			case bool(vol.External):
				add(key, "External volume "+v.Source+" is looked up on the node each task runs on; it has to exist there.")
			case (vol.Driver == "" || vol.Driver == "local") && len(vol.DriverOpts) == 0:
				add(key, "Volume "+v.Source+" is local to each node: a task that moves to another node starts with an empty volume. "+
					"Use shared storage (volume driver options) or pin the service with a placement constraint.")
			}
		case types.VolumeTypeBind:
			if !onOneNode {
				add(key, "Bind mount of "+v.Source+": the path must exist on every node the service can run on, Swarm does not create it, "+
					"and each node has its own content.")
			}
		}
	}
	return out
}

// networkWarnings lists the networks whose settings cost something on a
// Swarm.
func networkWarnings(project *types.Project) []Warning {
	var out []Warning
	for _, name := range sortedKeys(project.Networks) {
		nw := project.Networks[name]
		if _, ok := nw.DriverOpts["encrypted"]; ok && !bool(nw.External) {
			out = append(out, Warning{Key: "networks." + name, Message: "Encrypted overlay traffic between nodes is noticeably slower than unencrypted traffic."})
		}
	}
	return out
}
