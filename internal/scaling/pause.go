package scaling

import (
	"errors"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Check loads the scaling rules of stack name and returns them when the
// auto-scaler may act on them. It returns a pause instead while the stack
// files changed outside Dockmesh, the default override was not confirmed
// by a manual deploy or the rules don't validate: a replica is built from
// the files on disk with nobody there to review them. A nil config and
// pause means the stack has no enabled rules.
func Check(sm *stacks.Manager, name string) (*ScalingConfig, *stacks.ScalerPause, error) {
	dir, err := sm.Dir(name)
	if err != nil {
		return nil, nil, err
	}
	meta, err := LoadMeta(dir)
	if err != nil {
		return nil, &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: err.Error()}, nil
	}
	if meta == nil || meta.Scaling == nil || !meta.Scaling.Enabled || len(meta.Scaling.Rules) == 0 {
		return nil, nil, nil
	}
	changed, err := sm.ModifiedOutside(name)
	if err != nil {
		return nil, nil, err
	}
	if len(changed) > 0 {
		return nil, &stacks.ScalerPause{Reason: stacks.PauseModifiedOutside, Files: changed}, nil
	}
	if err := meta.Scaling.Validate(); err != nil {
		return nil, &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: err.Error()}, nil
	}
	if err := sm.CheckOverride(name, meta.ActiveEnvironment); err != nil {
		if !errors.Is(err, stacks.ErrOverrideUnconfirmed) {
			return nil, &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: err.Error()}, nil
		}
		return nil, &stacks.ScalerPause{Reason: stacks.PauseOverrideUnconfirmed, Override: meta.ActiveEnvironment}, nil
	}
	return meta.Scaling, nil, nil
}
