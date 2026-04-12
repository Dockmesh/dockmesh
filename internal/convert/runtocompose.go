// Package convert turns a `docker run` command line into a compose YAML
// fragment. Implements concept §1.2 "docker run → Compose Konverter".
//
// Supported flags (common subset — MVP):
//   --name, -d/--detach, -p/--publish, -v/--volume, -e/--env,
//   --env-file, --restart, --network, -w/--workdir, -u/--user,
//   --privileged, --cap-add, --cap-drop, --label, --hostname,
//   --read-only, -i/-t/--interactive/--tty, --entrypoint
//
// Unknown flags are collected and returned in Warnings so the caller can
// surface them in the UI instead of silently dropping data.
package convert

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mattn/go-shellwords"
	"gopkg.in/yaml.v3"
)

type Result struct {
	YAML     string   `json:"yaml"`
	Warnings []string `json:"warnings,omitempty"`
}

// service is what we marshal to YAML. Order of fields controls key order.
type service struct {
	Image       string            `yaml:"image"`
	ContainerName string          `yaml:"container_name,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty"`
	Environment []string          `yaml:"environment,omitempty"`
	EnvFile     []string          `yaml:"env_file,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty"`
	Networks    []string          `yaml:"networks,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Restart     string            `yaml:"restart,omitempty"`
	User        string            `yaml:"user,omitempty"`
	WorkingDir  string            `yaml:"working_dir,omitempty"`
	Hostname    string            `yaml:"hostname,omitempty"`
	Privileged  bool              `yaml:"privileged,omitempty"`
	ReadOnly    bool              `yaml:"read_only,omitempty"`
	CapAdd      []string          `yaml:"cap_add,omitempty"`
	CapDrop     []string          `yaml:"cap_drop,omitempty"`
	StdinOpen   bool              `yaml:"stdin_open,omitempty"`
	TTY         bool              `yaml:"tty,omitempty"`
}

// Run parses a `docker run …` command line.
func Run(cmdline string) (*Result, error) {
	args, err := shellwords.Parse(cmdline)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// Strip leading "docker", "run" tokens if present.
	for len(args) > 0 && (args[0] == "docker" || args[0] == "sudo") {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}

	svc := service{}
	serviceName := ""
	var warnings []string
	var rest []string // positional args after IMAGE

	// Single-char boolean flags that take no value.
	boolShorts := map[byte]bool{'d': true, 'i': true, 't': true}

	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		// --long or --long=value
		if strings.HasPrefix(a, "--") {
			name := a[2:]
			value := ""
			if eq := strings.Index(name, "="); eq >= 0 {
				value = name[eq+1:]
				name = name[:eq]
			}
			needsValue := !isBoolFlag(name)
			if needsValue && value == "" && i+1 < len(args) {
				i++
				value = args[i]
			}
			applyFlag(&svc, &serviceName, &warnings, name, value)
			i++
			continue
		}
		// -x or combined -it — treat each char as its own short flag.
		short := a[1:]
		// Handle combined short bools first: only if every char is a bool short.
		allBool := len(short) > 0
		for j := 0; j < len(short); j++ {
			if !boolShorts[short[j]] {
				allBool = false
				break
			}
		}
		if allBool {
			for j := 0; j < len(short); j++ {
				applyShort(&svc, &warnings, string(short[j]), "")
			}
			i++
			continue
		}
		// Non-bool short flag, possibly -pVALUE or -p VALUE
		shortName := string(short[0])
		value := ""
		if len(short) > 1 {
			value = short[1:]
		} else if i+1 < len(args) {
			i++
			value = args[i]
		}
		applyShort(&svc, &warnings, shortName, value)
		i++
	}

	if i >= len(args) {
		return nil, errors.New("no image specified")
	}
	svc.Image = args[i]
	rest = args[i+1:]
	if len(rest) > 0 {
		svc.Command = rest
	}
	svc.ContainerName = serviceName

	// Pick a service key: container name if provided, otherwise first
	// component of the image repo.
	key := serviceName
	if key == "" {
		key = imageToKey(svc.Image)
	}

	doc := map[string]any{
		"services": map[string]any{
			key: svc,
		},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	sort.Strings(warnings)
	return &Result{YAML: string(out), Warnings: warnings}, nil
}

func isBoolFlag(name string) bool {
	switch name {
	case "detach", "interactive", "tty", "privileged", "read-only", "rm", "init":
		return true
	}
	return false
}

func applyFlag(s *service, name *string, warnings *[]string, flag, value string) {
	switch flag {
	case "name":
		*name = value
	case "detach":
		// ignored — compose implies managed lifecycle
	case "publish", "p":
		s.Ports = append(s.Ports, value)
	case "volume", "v":
		s.Volumes = append(s.Volumes, value)
	case "env", "e":
		s.Environment = append(s.Environment, value)
	case "env-file":
		s.EnvFile = append(s.EnvFile, value)
	case "restart":
		s.Restart = value
	case "network":
		s.Networks = append(s.Networks, value)
	case "workdir", "w":
		s.WorkingDir = value
	case "user", "u":
		s.User = value
	case "privileged":
		s.Privileged = true
	case "cap-add":
		s.CapAdd = append(s.CapAdd, value)
	case "cap-drop":
		s.CapDrop = append(s.CapDrop, value)
	case "label", "l":
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		k, v, _ := strings.Cut(value, "=")
		s.Labels[k] = v
	case "hostname", "h":
		s.Hostname = value
	case "read-only":
		s.ReadOnly = true
	case "interactive":
		s.StdinOpen = true
	case "tty":
		s.TTY = true
	case "entrypoint":
		s.Entrypoint = []string{value}
	case "rm", "init":
		// silently ignored — compose managed
	default:
		*warnings = append(*warnings, "unsupported flag: --"+flag)
	}
}

func applyShort(s *service, warnings *[]string, flag, value string) {
	switch flag {
	case "d":
	case "i":
		s.StdinOpen = true
	case "t":
		s.TTY = true
	case "p":
		s.Ports = append(s.Ports, value)
	case "v":
		s.Volumes = append(s.Volumes, value)
	case "e":
		s.Environment = append(s.Environment, value)
	case "u":
		s.User = value
	case "w":
		s.WorkingDir = value
	case "l":
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		k, v, _ := strings.Cut(value, "=")
		s.Labels[k] = v
	case "h":
		s.Hostname = value
	default:
		*warnings = append(*warnings, "unsupported flag: -"+flag)
	}
}

func imageToKey(image string) string {
	// nginx:alpine → nginx, ghcr.io/foo/bar:tag → bar
	ref := image
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if col := strings.LastIndex(ref, ":"); col > strings.LastIndex(ref, "/") {
		ref = ref[:col]
	}
	if sl := strings.LastIndex(ref, "/"); sl >= 0 {
		ref = ref[sl+1:]
	}
	ref = strings.ToLower(ref)
	if ref == "" {
		return "app"
	}
	return ref
}
