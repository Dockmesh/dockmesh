package convert

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	dtypes "github.com/docker/docker/api/types"
	"gopkg.in/yaml.v3"
)

// composeProject is the top-level shape we marshal when reconstructing a
// compose project from running containers. Network and volume top-level
// blocks are omitted on purpose — they get re-derived from the per-
// service references during deploy, which keeps the recovered compose
// file tight and avoids leaking project-internal default network defs
// the operator never wrote by hand.
type composeProject struct {
	Services map[string]service `yaml:"services"`
}

// FromContainers reconstructs a compose.yaml from one or more containers
// that share a com.docker.compose.project label. Used by the stack
// recovery flow when the on-disk compose file is missing but the
// containers are still running (e.g. after a Docker Desktop reset that
// kept the data volume but wiped the dockmesh stacks dir).
//
// The reconstruction is a best-effort approximation — Docker's inspect
// data is rich enough to recover image/env/ports/volumes/restart/labels
// but loses anything the user wrote in the original compose that isn't
// reflected at runtime (comments, x-* extensions, anchors). The caller
// should always present the result to the operator for review before
// committing.
//
// projectName is required (passed in rather than read from labels) so
// the caller controls naming when several containers might disagree.
func FromContainers(projectName string, insp []dtypes.ContainerJSON) (*Result, error) {
	if projectName == "" {
		return nil, errors.New("project name is required")
	}
	if len(insp) == 0 {
		return nil, errors.New("no containers to reconstruct from")
	}

	// Group containers by compose service label. A service can have N
	// replicas; we take the first as the template and warn if the others
	// drift in any field that affects the compose definition.
	byService := map[string][]dtypes.ContainerJSON{}
	for _, c := range insp {
		svc := strings.TrimSpace(c.Config.Labels["com.docker.compose.service"])
		if svc == "" {
			// Container with the project label but no service label —
			// likely something an admin docker-run'd into the project
			// network manually. Skip it; not part of the compose spec.
			continue
		}
		byService[svc] = append(byService[svc], c)
	}
	if len(byService) == 0 {
		return nil, errors.New("no containers carry a com.docker.compose.service label")
	}

	proj := composeProject{Services: map[string]service{}}
	var warnings []string

	for svcName, cs := range byService {
		template := cs[0]
		s, w := serviceFromInspect(svcName, template)
		proj.Services[svcName] = s
		warnings = append(warnings, w...)

		if len(cs) > 1 {
			for _, replica := range cs[1:] {
				if replica.Config.Image != template.Config.Image {
					warnings = append(warnings, fmt.Sprintf(
						"service %s: replicas have differing images (%s vs %s); kept the first",
						svcName, template.Config.Image, replica.Config.Image))
				}
			}
			warnings = append(warnings, fmt.Sprintf(
				"service %s: %d replicas detected; compose entry written once (deploy with `replicas` or `dmctl stack scale` to recreate)",
				svcName, len(cs)))
		}
	}

	out, err := yaml.Marshal(proj)
	if err != nil {
		return nil, fmt.Errorf("marshal compose: %w", err)
	}
	sort.Strings(warnings)
	return &Result{YAML: string(out), Warnings: warnings}, nil
}

// serviceFromInspect maps one ContainerJSON onto a compose service.
// Fields are translated conservatively — anything we can't represent
// cleanly in compose v3 ends up in Warnings rather than silently lost.
func serviceFromInspect(svcName string, c dtypes.ContainerJSON) (service, []string) {
	s := service{Image: c.Config.Image}
	var warnings []string

	if c.Name != "" {
		s.ContainerName = strings.TrimPrefix(c.Name, "/")
	}

	if len(c.Config.Entrypoint) > 0 {
		s.Entrypoint = []string(c.Config.Entrypoint)
	}
	if len(c.Config.Cmd) > 0 {
		s.Command = []string(c.Config.Cmd)
	}

	for _, e := range c.Config.Env {
		if e == "" || !strings.Contains(e, "=") {
			continue
		}
		s.Environment = append(s.Environment, e)
	}
	sort.Strings(s.Environment)

	if c.HostConfig != nil {
		var ports []string
		for containerPort, binds := range c.HostConfig.PortBindings {
			cp := containerPort.Port()
			proto := containerPort.Proto()
			for _, b := range binds {
				host := b.HostPort
				if host == "" {
					continue
				}
				if proto == "tcp" {
					ports = append(ports, fmt.Sprintf("%s:%s", host, cp))
				} else {
					ports = append(ports, fmt.Sprintf("%s:%s/%s", host, cp, proto))
				}
			}
		}
		sort.Strings(ports)
		s.Ports = ports
	}

	if c.HostConfig != nil {
		var vols []string
		for _, b := range c.HostConfig.Binds {
			vols = append(vols, b)
		}
		for _, m := range c.Mounts {
			switch m.Type {
			case "bind":
				if m.RW {
					vols = append(vols, fmt.Sprintf("%s:%s", m.Source, m.Destination))
				} else {
					vols = append(vols, fmt.Sprintf("%s:%s:ro", m.Source, m.Destination))
				}
			case "volume":
				if m.Name != "" {
					if m.RW {
						vols = append(vols, fmt.Sprintf("%s:%s", m.Name, m.Destination))
					} else {
						vols = append(vols, fmt.Sprintf("%s:%s:ro", m.Name, m.Destination))
					}
				}
			case "tmpfs":
				warnings = append(warnings, fmt.Sprintf(
					"service %s: tmpfs mount %s preserved as warning only — recreate via compose `tmpfs:` block manually",
					svcName, m.Destination))
			}
		}
		seen := map[string]bool{}
		uniq := vols[:0]
		for _, v := range vols {
			if !seen[v] {
				seen[v] = true
				uniq = append(uniq, v)
			}
		}
		sort.Strings(uniq)
		s.Volumes = uniq
	}

	if c.HostConfig != nil && c.HostConfig.RestartPolicy.Name != "" {
		switch string(c.HostConfig.RestartPolicy.Name) {
		case "no", "always", "unless-stopped":
			s.Restart = string(c.HostConfig.RestartPolicy.Name)
		case "on-failure":
			if c.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
				s.Restart = fmt.Sprintf("on-failure:%d", c.HostConfig.RestartPolicy.MaximumRetryCount)
			} else {
				s.Restart = "on-failure"
			}
		}
	}

	if c.Config.User != "" {
		s.User = c.Config.User
	}
	if c.Config.WorkingDir != "" {
		s.WorkingDir = c.Config.WorkingDir
	}
	if c.Config.Hostname != "" && c.Config.Hostname != strings.TrimPrefix(c.Name, "/") {
		// Docker auto-sets hostname to the container ID short hash if
		// not specified; only emit it if it differs from container_name.
		s.Hostname = c.Config.Hostname
	}
	if c.HostConfig != nil && c.HostConfig.Privileged {
		s.Privileged = true
	}
	if c.HostConfig != nil && c.HostConfig.ReadonlyRootfs {
		s.ReadOnly = true
	}
	if c.HostConfig != nil {
		s.CapAdd = []string(c.HostConfig.CapAdd)
		s.CapDrop = []string(c.HostConfig.CapDrop)
	}
	if c.Config.Tty {
		s.TTY = true
	}
	if c.Config.OpenStdin {
		s.StdinOpen = true
	}

	if len(c.Config.Labels) > 0 {
		out := map[string]string{}
		for k, v := range c.Config.Labels {
			if strings.HasPrefix(k, "com.docker.") || strings.HasPrefix(k, "dockmesh.") {
				continue
			}
			out[k] = v
		}
		if len(out) > 0 {
			s.Labels = out
		}
	}

	if c.NetworkSettings != nil {
		var nets []string
		for n := range c.NetworkSettings.Networks {
			if strings.HasSuffix(n, "_default") || n == "bridge" || n == "host" || n == "none" {
				continue
			}
			nets = append(nets, n)
		}
		sort.Strings(nets)
		s.Networks = nets
	}

	return s, warnings
}
