package compose

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// LoadProject parses <stackDir>/compose.yaml with stackName as the project name.
// It resolves .env relative to the stack directory (same semantics as docker compose).
func LoadProject(ctx context.Context, stackDir, stackName string) (*types.Project, error) {
	composePath := filepath.Join(stackDir, "compose.yaml")
	opts, err := cli.NewProjectOptions(
		[]string{composePath},
		cli.WithName(stackName),
		cli.WithWorkingDirectory(stackDir),
		cli.WithDotEnv,
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
