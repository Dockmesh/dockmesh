package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/scaling"
)

// GlobalVars lists the global environment variables; nil means none.
type GlobalVars func(context.Context) ([]globalenv.Var, error)

// Spec is what a deploy hands to the host: the compose file with the
// stack's active override merged in and the .env behind the globals.
type Spec struct {
	Compose  string
	Env      string
	Override string
}

// MergeGlobalEnv puts the global variables in front of the stack's .env,
// so a stack line with the same key wins. A secret that can't be decrypted
// fails the deploy instead of falling back to a compose default.
func MergeGlobalEnv(vars []globalenv.Var, stackEnv string) (string, error) {
	if len(vars) == 0 {
		return stackEnv, nil
	}
	var b strings.Builder
	for _, v := range vars {
		if v.Unreadable {
			return "", fmt.Errorf("global variable %s can't be decrypted; check the secrets key or set the value again", v.Key)
		}
		fmt.Fprintf(&b, "%s=%s\n", v.Key, v.Value)
	}
	if stackEnv != "" {
		if !strings.HasSuffix(stackEnv, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(stackEnv)
	}
	return b.String(), nil
}

// ActiveOverride is the default environment override from the stack's
// meta file, "" when there is none.
func ActiveOverride(dir string) string {
	meta, err := scaling.LoadMeta(dir)
	if err != nil || meta == nil {
		return ""
	}
	return meta.ActiveEnvironment
}

// Resolve builds the spec for deploys that run without a request, such as
// git auto-deploys and migrations: globals plus the stack's default
// override, the same as a manual deploy without ?environment.
func Resolve(ctx context.Context, globals GlobalVars, dir, name, baseCompose, stackEnv string) (Spec, error) {
	spec := Spec{Compose: baseCompose, Env: stackEnv}
	if globals != nil {
		vars, err := globals(ctx)
		if err != nil {
			return Spec{}, fmt.Errorf("read global environment: %w", err)
		}
		if spec.Env, err = MergeGlobalEnv(vars, stackEnv); err != nil {
			return Spec{}, err
		}
	}
	spec.Override = ActiveOverride(dir)
	if spec.Override == "" {
		return spec, nil
	}
	_, merged, err := compose.MergeEnvironment(ctx, dir, name, spec.Env, spec.Override)
	if err != nil {
		return Spec{}, fmt.Errorf("environment %s: %w", spec.Override, err)
	}
	spec.Compose = merged
	return spec, nil
}
