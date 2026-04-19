package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// ErrEnvironmentNotFound is returned by MergeEnvironment when the
// caller requested an override that doesn't have a corresponding
// `compose.<name>.yaml` next to the base compose file.
var ErrEnvironmentNotFound = errors.New("environment override not found")

// envFilePattern matches the compose.<env>.yaml override filenames we
// recognise. Anchored so only that exact shape counts; we don't pick
// up `compose.yaml` itself or random `compose.foo.bar.yaml` variants.
//
// Allowed env names: lowercase letters, digits, dashes, underscores.
// Enforced at both write-time (API validation) and read-time
// (discovery) so names stay filesystem-safe across the OS targets
// Dockmesh runs on.
var envFilePattern = regexp.MustCompile(`^compose\.([a-z0-9][a-z0-9_-]*)\.yaml$`)

// DiscoverEnvironments scans a stack directory and returns every
// override name that has a `compose.<name>.yaml` file on disk, sorted
// alphabetically. Empty slice means the stack only has the base file.
// P.12.8.
func DiscoverEnvironments(stackDir string) ([]string, error) {
	entries, err := os.ReadDir(stackDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := envFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out, nil
}

// ValidateEnvironmentName enforces the naming rules DiscoverEnvironments
// relies on. Callers hitting the set-active-environment endpoint get a
// 400 instead of writing a garbage name into the meta file.
func ValidateEnvironmentName(name string) error {
	if name == "" {
		return nil // empty means "no override" — always allowed
	}
	if !envFilePattern.MatchString("compose." + name + ".yaml") {
		return fmt.Errorf("invalid environment name %q (allowed: lowercase letters, digits, '-' and '_')", name)
	}
	return nil
}

// MergeEnvironment loads compose.yaml optionally overlaid with
// compose.<overrideName>.yaml via compose-go's native multi-file merge,
// and returns the resulting project plus its re-serialised YAML.
//
// The re-serialised YAML is what the host abstraction ships to the
// deploy target (local or agent) — downstream code sees a single
// flattened compose file and doesn't need to know about overrides.
// P.12.8.
//
// When overrideName is empty or no matching file exists, returns an
// error for the "requested-but-missing" case (caller bug); the "not
// requested" case just loads the base.
func MergeEnvironment(ctx context.Context, stackDir, stackName, envContent, overrideName string) (*types.Project, string, error) {
	files := []string{filepath.Join(stackDir, "compose.yaml")}
	if overrideName != "" {
		if err := ValidateEnvironmentName(overrideName); err != nil {
			return nil, "", err
		}
		overlay := filepath.Join(stackDir, "compose."+overrideName+".yaml")
		if _, err := os.Stat(overlay); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("%w: %s", ErrEnvironmentNotFound, overlay)
			}
			return nil, "", err
		}
		files = append(files, overlay)
	}

	envSlice := parseEnvContent(envContent)
	opts, err := cli.NewProjectOptions(
		files,
		cli.WithName(stackName),
		cli.WithWorkingDirectory(stackDir),
		cli.WithEnv(envSlice),
		cli.WithResolvedPaths(true),
	)
	if err != nil {
		return nil, "", fmt.Errorf("project options: %w", err)
	}
	proj, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("load project: %w", err)
	}
	out, err := proj.MarshalYAML()
	if err != nil {
		return nil, "", fmt.Errorf("marshal merged yaml: %w", err)
	}
	return proj, strings.TrimRight(string(out), "\n") + "\n", nil
}
