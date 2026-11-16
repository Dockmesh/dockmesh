package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// LoadProject parses <stackDir>/compose.yaml with stackName as the project
// name. Env vars are passed explicitly (never read from a .env file on disk)
// so the secrets service can decrypt .env.age into memory and hand us the
// plaintext without touching the filesystem.
func LoadProject(ctx context.Context, stackDir, stackName, envContent string) (*types.Project, error) {
	return loadChecked(ctx, stackDir, []string{filepath.Join(stackDir, "compose.yaml")}, stackName, envContent)
}

// loadChecked loads the compose files in stackDir after checking every
// file they make compose-go read: include and extends targets before the
// load, env and label files before compose-go resolves them. All of them
// must be regular files inside stackDir.
func loadChecked(ctx context.Context, stackDir string, composeFiles []string, stackName, envContent string) (*types.Project, error) {
	files, err := openStackFiles(stackDir)
	if err != nil {
		return nil, err
	}
	defer files.Close()
	for i, f := range composeFiles {
		if !filepath.IsAbs(f) {
			composeFiles[i] = filepath.Join(files.dir, f)
		}
	}
	envSlice := parseEnvContent(envContent)
	opts, err := cli.NewProjectOptions(
		composeFiles,
		cli.WithName(stackName),
		cli.WithWorkingDirectory(files.dir),
		cli.WithEnv(envSlice),
		cli.WithResolvedPaths(true),
		cli.WithoutEnvironmentResolution,
		cli.WithLoadOptions(func(o *loader.Options) { o.SkipResolveLabels = true }),
	)
	if err != nil {
		return nil, fmt.Errorf("project options: %w", err)
	}
	env := opts.Environment.Clone()
	env[consts.ComposeProjectName] = stackName
	w := &refWalker{files: files, name: stackName, seen: map[string]bool{}}
	for _, f := range composeFiles {
		if err := w.walk(ctx, f, files.dir, env, false); err != nil {
			return nil, err
		}
	}
	proj, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("load project: %w", err)
	}
	if err := checkProjectFileRefs(files, proj); err != nil {
		return nil, err
	}
	proj, err = proj.WithServicesEnvironmentResolved(false)
	if err != nil {
		return nil, fmt.Errorf("load project: %w", err)
	}
	proj, err = proj.WithServicesLabelsResolved(false)
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
