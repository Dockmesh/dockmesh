package scaling

import (
	"context"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestCheckedScaleRefusesActiveMigration(t *testing.T) {
	errStore := errors.New("store down")
	tests := []struct {
		name      string
		active    bool
		hookErr   error
		wantErr   error
		wantScale bool
	}{
		{name: "no migration", wantScale: true},
		{name: "migration or rollback unfinished", active: true, wantErr: errMigrating},
		{name: "store error", hookErr: errStore, wantErr: errStore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, dir, database := scalingEnvDB(t)
			saveMeta(t, m, dir, validRules)
			deployments := stacks.NewDeploymentStore(database)
			if err := deployments.Set(context.Background(), "app", "agent-1", "deployed"); err != nil {
				t.Fatal(err)
			}
			locks := stacklock.New()
			target := &fakeTarget{replicas: 1}
			lockHeld := false
			migrating := func(_ context.Context, stack string) (bool, error) {
				if stack != "app" {
					t.Errorf("hook asked for stack %q", stack)
				}
				if release, err := locks.TryLock(target.LockKey(), stacklock.OpDeploy); err == nil {
					release()
				} else {
					lockHeld = true
				}
				return tt.active, tt.hookErr
			}

			err := CheckedScale(m, locks, deployments, migrating)(context.Background(), "app", target, 1, 2)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && err != nil {
				t.Fatalf("err = %v", err)
			}
			if !lockHeld {
				t.Error("migration checked without the stack lock")
			}
			if scaled := target.replicas == 2; scaled != tt.wantScale {
				t.Errorf("scaled = %v, want %v", scaled, tt.wantScale)
			}
			release, err := locks.TryLock(target.LockKey(), stacklock.OpDeploy)
			if err != nil {
				t.Fatalf("stack lock kept: %v", err)
			}
			release()
		})
	}
}
