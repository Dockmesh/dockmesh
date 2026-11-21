// Package swarmgolden holds the golden cases of the compose → Swarm
// converter: compose files next to the service and network specs that
// `docker stack deploy` sent to a real Swarm for them. Tests load the
// cases and compare the converter's output with the recording; how cases
// are recorded and added is in README.md.
package swarmgolden

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"gopkg.in/yaml.v3"
)

// StackPrefix is prepended to a case name to give the stack name it was
// recorded with.
const StackPrefix = "gt-"

// Case is one recorded golden case.
type Case struct {
	Name    string
	Dir     string
	Compose []byte
	// Env holds the variables of golden.env, which the recording exported
	// into the CLI's environment for interpolation.
	Env map[string]string
	// StackConfig is the output of `docker stack config` for the case.
	StackConfig []byte
	Services    []swarm.ServiceSpec
	Networks    []network.CreateRequest
	// ExternalNetworks are the networks the recording created before the
	// deploy because the case declares them external.
	ExternalNetworks []string
	Meta             Meta
}

// Stack is the stack name the case was recorded with.
func (c Case) Stack() string { return StackPrefix + c.Name }

// Meta is a case's optional case.yaml.
type Meta struct {
	// Pending lists spec paths a later converter piece produces; the
	// comparison skips them.
	Pending []string `yaml:"pending"`
	// Accepted lists permanent differences to the CLI, each with the
	// decision behind it.
	Accepted []Accepted `yaml:"accepted"`
}

// Accepted is a spec path where the converter deliberately differs from
// the CLI.
type Accepted struct {
	Path   string `yaml:"path"`
	Reason string `yaml:"reason"`
}

// Ignored returns the spec paths the comparison skips for this case.
func (m Meta) Ignored() []string {
	out := slices.Clone(m.Pending)
	for _, a := range m.Accepted {
		out = append(out, a.Path)
	}
	return out
}

type request struct {
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
}

// LoadAll loads every case below dir, sorted by name.
func LoadAll(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", e.Name(), err)
		}
		out = append(out, c)
	}
	return out, nil
}

// Load reads one case directory. Specs are decoded strictly, so a field
// the CLI sends that the API types don't know fails the load.
func Load(dir string) (Case, error) {
	c := Case{Name: filepath.Base(dir), Dir: dir}
	var err error
	if c.Compose, err = os.ReadFile(filepath.Join(dir, "compose.yaml")); err != nil {
		return c, err
	}
	if c.Env, err = readEnv(filepath.Join(dir, "golden.env")); err != nil {
		return c, err
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "external-networks")); err == nil {
		c.ExternalNetworks = strings.Fields(string(raw))
	} else if !os.IsNotExist(err) {
		return c, err
	}
	if c.StackConfig, err = os.ReadFile(filepath.Join(dir, "recorded", "stack-config.yaml")); err != nil {
		return c, err
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "case.yaml")); err == nil {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&c.Meta); err != nil {
			return c, fmt.Errorf("case.yaml: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	reqs, err := readRequests(filepath.Join(dir, "recorded", "requests.jsonl"))
	if err != nil {
		return c, err
	}
	for _, r := range reqs {
		switch {
		case strings.HasSuffix(r.Path, "/services/create"):
			var s swarm.ServiceSpec
			if err := strictJSON(r.Body, &s); err != nil {
				return c, fmt.Errorf("service spec: %w", err)
			}
			c.Services = append(c.Services, s)
		case strings.HasSuffix(r.Path, "/networks/create"):
			var n network.CreateRequest
			if err := strictJSON(r.Body, &n); err != nil {
				return c, fmt.Errorf("network: %w", err)
			}
			c.Networks = append(c.Networks, n)
		default:
			return c, fmt.Errorf("unexpected recorded request %s", r.Path)
		}
	}
	sort.Slice(c.Services, func(i, j int) bool { return c.Services[i].Name < c.Services[j].Name })
	sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Name < c.Networks[j].Name })
	return c, nil
}

func readRequests(path string) ([]request, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []request
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r request
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func readEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("golden.env: %q is not KEY=VALUE", line)
		}
		out[k] = v
	}
	return out, nil
}

func strictJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
