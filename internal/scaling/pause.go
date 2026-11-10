package scaling

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Verdict is what the auto-scaler may do with a stack. Rules is nil when
// the stack has no enabled rules or Pause is set. Files holds the stack
// files the verdict was made on; a scale builds from Files.Detail only.
type Verdict struct {
	Rules *ScalingConfig
	Pause *stacks.ScalerPause
	Files *stacks.Snapshot
}

// Check reads the stack files of name once and returns its rules when the
// auto-scaler may act on them. It pauses the stack instead while the files
// changed outside Dockmesh, the default override was not confirmed by a
// manual deploy or the rules don't validate: a replica is built from the
// files on disk with nobody there to review them.
func Check(sm *stacks.Manager, name string) (Verdict, error) {
	dir, err := sm.Dir(name)
	if err != nil {
		return Verdict{}, err
	}
	if _, err := os.Lstat(filepath.Join(dir, stacks.MetaFileName)); errors.Is(err, os.ErrNotExist) {
		return Verdict{}, nil
	}
	snap, err := sm.Snapshot(name)
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Files: snap}
	if snap.Meta == nil {
		return v, nil
	}
	var meta MetaFile
	if err := json.Unmarshal(snap.Meta, &meta); err != nil {
		v.Pause = &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: "parse " + stacks.MetaFileName + ": " + err.Error()}
		if len(snap.Changed) > 0 {
			v.Pause = &stacks.ScalerPause{Reason: stacks.PauseModifiedOutside, Files: snap.Changed}
		}
		return v, nil //nolint:nilerr // a meta file that doesn't parse pauses the stack
	}
	if meta.Scaling == nil || !meta.Scaling.Enabled || len(meta.Scaling.Rules) == 0 {
		return v, nil
	}
	if len(snap.Changed) > 0 {
		v.Pause = &stacks.ScalerPause{Reason: stacks.PauseModifiedOutside, Files: snap.Changed}
		return v, nil
	}
	if err := meta.Scaling.Validate(); err != nil {
		v.Pause = &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: err.Error()}
		return v, nil //nolint:nilerr // invalid rules pause the stack
	}
	if err := sm.CheckOverride(name, meta.ActiveEnvironment); err != nil {
		if !errors.Is(err, stacks.ErrOverrideUnconfirmed) {
			v.Pause = &stacks.ScalerPause{Reason: stacks.PauseInvalidRules, Error: err.Error()}
			return v, nil
		}
		v.Pause = &stacks.ScalerPause{Reason: stacks.PauseOverrideUnconfirmed, Override: meta.ActiveEnvironment}
		return v, nil
	}
	v.Rules = meta.Scaling
	return v, nil
}
