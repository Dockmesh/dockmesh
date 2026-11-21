package swarmconvert

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"gopkg.in/yaml.v3"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Adopted is an External stack written down as a compose file and the
// stack's environment file. Environment values go to the environment
// file, which is kept encrypted, and the compose file refers to them.
type Adopted struct {
	Compose string
	Env     string
	// EnvKeys are the variables of Env, values left out.
	EnvKeys []string
	// Findings are the parts of the running services the file can't carry.
	Findings []Finding
	// KeptNetworks are networks the CLI created; the file uses them as
	// external ones, so Dockmesh never changes or removes them.
	KeptNetworks []string
}

var envVarUnsafe = regexp.MustCompile(`[^A-Z0-9_]+`)

// AdoptStack writes the services of stack as a compose file. It covers
// what Convert maps; Diff names the rest per service.
func AdoptStack(stack string, services []swarm.Service, networks []network.Summary) (*Adopted, error) {
	a := &Adopted{}
	netByID := map[string]network.Summary{}
	for _, n := range networks {
		netByID[n.ID] = n
		netByID[n.Name] = n
	}
	top := map[string]any{}
	svcs := map[string]any{}
	topNets := map[string]any{}
	topVols := map[string]any{}
	topSecrets := map[string]any{}
	topConfigs := map[string]any{}
	var env []string
	envNames := map[string]bool{}
	netKeys, secretKeys, configKeys := map[string]string{}, map[string]string{}, map[string]string{}
	// toEnv moves value to the environment file, which is stored
	// encrypted, and returns the reference the compose file uses.
	toEnv := func(service, key, varName, value string) (string, bool) {
		if strings.ContainsAny(value, "\r\n") || strings.TrimRight(value, " \t") != value {
			a.Findings = append(a.Findings, Finding{Service: service, Key: key,
				Message: "The value has a line break or trailing blanks and can't go into the environment file; set it there by hand."})
			return "", false
		}
		v0 := envVarUnsafe.ReplaceAllString(strings.ToUpper(varName), "_")
		if v0 == "" || v0[0] >= '0' && v0[0] <= '9' {
			v0 = "_" + v0
		}
		vn := v0
		for i := 2; envNames[vn]; i++ {
			vn = v0 + "_" + strconv.Itoa(i)
		}
		envNames[vn] = true
		env = append(env, vn+"="+value)
		a.EnvKeys = append(a.EnvKeys, vn)
		return "${" + vn + "}", true
	}
	// driverOpts writes driver options through the environment file:
	// they often carry credentials, e.g. of a CIFS volume.
	driverOpts := func(service, key, prefix string, opts map[string]string) map[string]string {
		out := map[string]string{}
		for _, k := range slices.Sorted(maps.Keys(opts)) {
			if ref, ok := toEnv(service, key+" "+k, prefix+"_"+k, opts[k]); ok {
				out[esc(k)] = ref
			}
		}
		return out
	}
	prefix := stack + "_"

	for _, s := range services {
		spec := s.Spec
		name, ok := strings.CutPrefix(spec.Name, prefix)
		if !ok || name == "" {
			a.Findings = append(a.Findings, Finding{Service: spec.Name, Key: "name", Message: "The service name does not start with " + prefix + "; it is left out."})
			continue
		}
		c := spec.TaskTemplate.ContainerSpec
		if c == nil {
			a.Findings = append(a.Findings, Finding{Service: name, Key: "runtime", Message: "Only container services can be adopted; it is left out."})
			continue
		}
		out := map[string]any{}
		image := spec.Labels[LabelImage]
		if image == "" {
			image = stripDigest(c.Image)
		}
		out["image"] = esc(image)
		if len(c.Command) > 0 {
			out["entrypoint"] = escAll(c.Command)
		}
		if len(c.Args) > 0 {
			out["command"] = escAll(c.Args)
		}
		if len(c.Env) > 0 {
			var list []string
			for _, kv := range c.Env {
				k, v, hasValue := strings.Cut(kv, "=")
				if !hasValue {
					list = append(list, esc(k))
					continue
				}
				ref, ok := toEnv(name, "environment "+k, name+"_"+k, v)
				if !ok {
					list = append(list, esc(k)+"=")
					continue
				}
				list = append(list, esc(k)+"="+ref)
			}
			out["environment"] = list
		}
		if l := without(c.Labels, LabelNamespace); len(l) > 0 {
			out["labels"] = escMap(l)
		}
		if hc := c.Healthcheck; hc != nil {
			h := map[string]any{}
			if len(hc.Test) == 1 && hc.Test[0] == "NONE" {
				h["disable"] = true
			} else {
				if len(hc.Test) > 0 {
					h["test"] = escAll(hc.Test)
				}
				putDuration(h, "interval", hc.Interval)
				putDuration(h, "timeout", hc.Timeout)
				putDuration(h, "start_period", hc.StartPeriod)
				putDuration(h, "start_interval", hc.StartInterval)
				if hc.Retries != 0 {
					h["retries"] = hc.Retries
				}
			}
			out["healthcheck"] = h
		}
		if e := spec.EndpointSpec; e != nil && len(e.Ports) > 0 {
			var ports []any
			for _, p := range e.Ports {
				pm := map[string]any{"target": p.TargetPort}
				if p.PublishedPort != 0 {
					pm["published"] = strconv.FormatUint(uint64(p.PublishedPort), 10)
				}
				if p.Protocol != "" {
					pm["protocol"] = string(p.Protocol)
				}
				if p.PublishMode != "" {
					pm["mode"] = string(p.PublishMode)
				}
				ports = append(ports, pm)
			}
			out["ports"] = ports
		}
		deploy := map[string]any{}
		switch m := spec.Mode; {
		case m.Global != nil:
			deploy["mode"] = "global"
		case m.Replicated != nil:
			if m.Replicated.Replicas != nil {
				deploy["replicas"] = *m.Replicated.Replicas
			}
		case m.GlobalJob != nil:
			deploy["mode"] = "global-job"
		case m.ReplicatedJob != nil:
			deploy["mode"] = "replicated-job"
			if n := m.ReplicatedJob.TotalCompletions; n != nil {
				deploy["replicas"] = *n
			}
			if !reflect.DeepEqual(m.ReplicatedJob.MaxConcurrent, m.ReplicatedJob.TotalCompletions) {
				a.Findings = append(a.Findings, Finding{Service: name, Key: "deploy.mode",
					Message: "The job runs more tasks at once than a compose file can say; a deploy sets both to deploy.replicas."})
			}
		default:
			a.Findings = append(a.Findings, Finding{Service: name, Key: "deploy.mode", Message: "The service mode can't be written to a compose file; it is left out."})
			continue
		}
		if e := spec.EndpointSpec; e != nil && e.Mode != "" && e.Mode != swarm.ResolutionModeVIP {
			deploy["endpoint_mode"] = string(e.Mode)
		}
		if l := without(spec.Labels, LabelNamespace, LabelImage, agents.LabelSpecHash); len(l) > 0 {
			deploy["labels"] = escMap(l)
		}
		if rp := spec.TaskTemplate.RestartPolicy; rp != nil {
			r := map[string]any{}
			if rp.Condition != "" {
				r["condition"] = string(rp.Condition)
			}
			if rp.Delay != nil {
				r["delay"] = rp.Delay.String()
			}
			if rp.MaxAttempts != nil {
				r["max_attempts"] = *rp.MaxAttempts
			}
			if rp.Window != nil {
				r["window"] = rp.Window.String()
			}
			deploy["restart_policy"] = r
		}
		if p := spec.TaskTemplate.Placement; p != nil {
			pl := map[string]any{}
			if len(p.Constraints) > 0 {
				pl["constraints"] = escAll(p.Constraints)
			}
			var prefs []any
			for _, pref := range p.Preferences {
				if pref.Spread != nil {
					prefs = append(prefs, map[string]any{"spread": esc(pref.Spread.SpreadDescriptor)})
				}
			}
			if len(prefs) > 0 {
				pl["preferences"] = prefs
			}
			if p.MaxReplicas != 0 {
				pl["max_replicas_per_node"] = p.MaxReplicas
			}
			if len(pl) > 0 {
				deploy["placement"] = pl
			}
		}
		if u := updateConfig(spec.UpdateConfig); u != nil {
			deploy["update_config"] = u
		}
		if u := updateConfig(spec.RollbackConfig); u != nil {
			deploy["rollback_config"] = u
		}
		if res := resources(spec.TaskTemplate.Resources); len(res) > 0 {
			deploy["resources"] = res
		}
		if len(deploy) > 0 {
			out["deploy"] = deploy
		}

		var vols []any
		for _, m := range c.Mounts {
			v := map[string]any{"type": string(m.Type), "target": esc(m.Target)}
			if m.ReadOnly {
				v["read_only"] = true
			}
			if m.Consistency != "" && m.Consistency != mount.ConsistencyDefault {
				v["consistency"] = string(m.Consistency)
			}
			switch m.Type {
			case mount.TypeVolume:
				if m.Source == "" {
					break
				}
				key, own := strings.CutPrefix(m.Source, prefix)
				vol := map[string]any{}
				if !own || key == "" {
					key = volumeKey(m.Source, topVols)
					vol["external"] = true
					vol["name"] = esc(m.Source)
				} else if prev, seen := topVols[key]; seen {
					vol = prev.(map[string]any)
				} else if o := m.VolumeOptions; o != nil {
					if l := without(o.Labels, LabelNamespace); len(l) > 0 {
						vol["labels"] = escMap(l)
					}
					if o.DriverConfig != nil {
						if o.DriverConfig.Name != "" {
							vol["driver"] = esc(o.DriverConfig.Name)
						}
						if len(o.DriverConfig.Options) > 0 {
							vol["driver_opts"] = driverOpts("", "volumes "+key+" driver_opts", "VOL_"+key, o.DriverConfig.Options)
						}
					}
				}
				topVols[key] = vol
				v["source"] = key
				if o := m.VolumeOptions; o != nil && (o.NoCopy || o.Subpath != "") {
					vo := map[string]any{}
					if o.NoCopy {
						vo["nocopy"] = true
					}
					if o.Subpath != "" {
						vo["subpath"] = esc(o.Subpath)
					}
					v["volume"] = vo
				}
			case mount.TypeBind:
				v["source"] = esc(m.Source)
				if m.BindOptions != nil && m.BindOptions.Propagation != "" {
					v["bind"] = map[string]any{"propagation": string(m.BindOptions.Propagation)}
				}
			case mount.TypeTmpfs:
				if m.TmpfsOptions != nil && m.TmpfsOptions.SizeBytes != 0 {
					v["tmpfs"] = map[string]any{"size": m.TmpfsOptions.SizeBytes}
				}
			case mount.TypeImage:
				v["source"] = esc(m.Source)
				if m.ImageOptions != nil && m.ImageOptions.Subpath != "" {
					v["image"] = map[string]any{"subpath": esc(m.ImageOptions.Subpath)}
				}
			default:
				a.Findings = append(a.Findings, Finding{Service: name, Key: "volumes " + m.Target, Message: "Mounts of type " + string(m.Type) + " are not carried over."})
				continue
			}
			vols = append(vols, v)
		}
		if len(vols) > 0 {
			out["volumes"] = vols
		}

		svcNets := map[string]any{}
		for _, att := range spec.TaskTemplate.Networks {
			n, ok := netByID[att.Target]
			if !ok {
				a.Findings = append(a.Findings, Finding{Service: name, Key: "networks", Message: "The service is attached to a network the manager did not list; the attachment is not carried over."})
				continue
			}
			key := objKey(stack, n.Name, netKeys)
			if _, seen := topNets[key]; !seen {
				topNets[key] = map[string]any{"external": true, "name": esc(n.Name)}
				if n.Labels[LabelNamespace] == stack {
					a.KeptNetworks = append(a.KeptNetworks, n.Name)
				}
			}
			var aliases []string
			for _, al := range att.Aliases {
				if al != name {
					aliases = append(aliases, esc(al))
				}
			}
			cfg := map[string]any{}
			if len(aliases) > 0 {
				cfg["aliases"] = aliases
			}
			if len(att.DriverOpts) > 0 {
				cfg["driver_opts"] = driverOpts(name, "networks "+key+" driver_opts", name+"_NET_"+key, att.DriverOpts)
			}
			if len(cfg) > 0 {
				svcNets[key] = cfg
			} else {
				svcNets[key] = nil
			}
		}
		if len(svcNets) > 0 {
			out["networks"] = svcNets
		}

		var secrets []any
		for _, r := range c.Secrets {
			if r == nil || r.File == nil {
				a.Findings = append(a.Findings, Finding{Service: name, Key: "secrets", Message: "A secret that is not mounted as a file is not carried over."})
				continue
			}
			key := objKey(stack, r.SecretName, secretKeys)
			topSecrets[key] = map[string]any{"external": true, "name": esc(r.SecretName)}
			secrets = append(secrets, fileRefOut(key, r.File.Name, r.File.UID, r.File.GID, uint32(r.File.Mode)))
		}
		if len(secrets) > 0 {
			out["secrets"] = secrets
		}
		var configs []any
		for _, r := range c.Configs {
			if r == nil || r.File == nil {
				a.Findings = append(a.Findings, Finding{Service: name, Key: "configs", Message: "A config that is not mounted as a file is not carried over."})
				continue
			}
			key := objKey(stack, r.ConfigName, configKeys)
			topConfigs[key] = map[string]any{"external": true, "name": esc(r.ConfigName)}
			configs = append(configs, fileRefOut(key, r.File.Name, r.File.UID, r.File.GID, uint32(r.File.Mode)))
		}
		if len(configs) > 0 {
			out["configs"] = configs
		}
		svcs[name] = out
	}
	if len(svcs) == 0 {
		return nil, fmt.Errorf("stack %s has no service that can be adopted", stack)
	}
	top["services"] = svcs
	for k, v := range map[string]map[string]any{"networks": topNets, "volumes": topVols, "secrets": topSecrets, "configs": topConfigs} {
		if len(v) > 0 {
			top[k] = v
		}
	}
	b, err := yaml.Marshal(top)
	if err != nil {
		return nil, err
	}
	a.Compose = string(b)
	if len(env) > 0 {
		a.Env = strings.Join(env, "\n") + "\n"
	}
	slices.Sort(a.KeptNetworks)
	return a, nil
}

// esc keeps compose-go from interpolating a value written from a spec.
func esc(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func escAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = esc(s)
	}
	return out
}

func escMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[esc(k)] = esc(v)
	}
	return out
}

func without(in map[string]string, keys ...string) map[string]string {
	out := maps.Clone(in)
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

func putDuration(m map[string]any, key string, d time.Duration) {
	if d != 0 {
		m[key] = d.String()
	}
}

func updateConfig(u *swarm.UpdateConfig) map[string]any {
	if u == nil {
		return nil
	}
	m := map[string]any{"parallelism": u.Parallelism}
	putDuration(m, "delay", u.Delay)
	putDuration(m, "monitor", u.Monitor)
	if u.FailureAction != "" {
		m["failure_action"] = string(u.FailureAction)
	}
	if u.MaxFailureRatio != 0 {
		m["max_failure_ratio"] = u.MaxFailureRatio
	}
	if u.Order != "" {
		m["order"] = string(u.Order)
	}
	return m
}

func resources(r *swarm.ResourceRequirements) map[string]any {
	out := map[string]any{}
	if r == nil {
		return out
	}
	if l := r.Limits; l != nil {
		m := map[string]any{}
		if l.NanoCPUs != 0 {
			m["cpus"] = cpus(l.NanoCPUs)
		}
		if l.MemoryBytes != 0 {
			m["memory"] = strconv.FormatInt(l.MemoryBytes, 10)
		}
		if l.Pids != 0 {
			m["pids"] = l.Pids
		}
		out["limits"] = m
	}
	if rv := r.Reservations; rv != nil {
		m := map[string]any{}
		if rv.NanoCPUs != 0 {
			m["cpus"] = cpus(rv.NanoCPUs)
		}
		if rv.MemoryBytes != 0 {
			m["memory"] = strconv.FormatInt(rv.MemoryBytes, 10)
		}
		var gen []any
		for _, g := range rv.GenericResources {
			if d := g.DiscreteResourceSpec; d != nil {
				gen = append(gen, map[string]any{"discrete_resource_spec": map[string]any{"kind": esc(d.Kind), "value": d.Value}})
			}
		}
		if len(gen) > 0 {
			m["generic_resources"] = gen
		}
		out["reservations"] = m
	}
	return out
}

func cpus(nano int64) string {
	return strconv.FormatFloat(float64(nano)/1e9, 'f', -1, 64)
}

var keyUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// volumeKey is the compose key of an external volume: its name, made
// unique among the keys taken.
func volumeKey(source string, taken map[string]any) string {
	key := keyUnsafe.ReplaceAllString(source, "_")
	base := key
	for i := 2; taken[key] != nil; i++ {
		key = base + "_" + strconv.Itoa(i)
	}
	return key
}

// objKey is the compose key of a network, secret or config named name:
// without the stack prefix where it has one, unless that key already
// stands for another name.
func objKey(stack, name string, taken map[string]string) string {
	key := name
	if k, ok := strings.CutPrefix(name, stack+"_"); ok && k != "" {
		key = k
	}
	if other, ok := taken[key]; ok && other != name {
		key = name
	}
	taken[key] = name
	return key
}

func fileRefOut(source, target, uid, gid string, mode uint32) map[string]any {
	m := map[string]any{"source": source}
	if target != "" && target != source {
		m["target"] = esc(target)
	}
	if uid != "" && uid != "0" {
		m["uid"] = esc(uid)
	}
	if gid != "" && gid != "0" {
		m["gid"] = esc(gid)
	}
	if mode != 0o444 {
		m["mode"] = mode
	}
	return m
}

func stripDigest(image string) string {
	if i := strings.Index(image, "@sha256:"); i > 0 {
		return image[:i]
	}
	return image
}
