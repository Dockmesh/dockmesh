package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// LoadProject parses <stackDir>/compose.yaml with stackName as the project
// name. Env vars are passed explicitly (never read from a .env file on disk)
// so the secrets service can decrypt .env.age into memory and hand us the
// plaintext without touching the filesystem.
func LoadProject(ctx context.Context, stackDir, stackName, envContent string) (*types.Project, error) {
	composePath := filepath.Join(stackDir, "compose.yaml")
	envSlice := parseEnvContent(envContent)
	opts, err := cli.NewProjectOptions(
		[]string{composePath},
		cli.WithName(stackName),
		cli.WithWorkingDirectory(stackDir),
		cli.WithEnv(envSlice),
		cli.WithResolvedPaths(true),
	)
	if err != nil {
		return nil, fmt.Errorf("project options: %w", err)
	}
	proj, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("load project: %w", err)
	}
	return proj, nil
}

// parseEnvContent turns a .env file string into the KEY=VALUE slice that
// compose-go expects. Comment lines (#...) and blank lines are skipped.
// Only simple KEY=VALUE form is supported — no export keyword, no quoting
// rules beyond what compose-go does with the values.
func parseEnvContent(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			continue
		}
		out = append(out, line)
	}
	return out
}
