package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/deploy"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/google/uuid"
)

// Service orchestrates stack migrations. Each migration runs as a
// goroutine that advances through the 10-phase state machine. Only
// one migration per stack is allowed at a time.
type Service struct {
	store       *Store
	hosts       Hosts
	stacks      *stacks.Manager
	deployments *stacks.DeploymentStore
	db          *sql.DB
	notifs      *notifications.Service
	globals     deploy.GlobalVars
	usage       ContainerUsage
	volumeIO    func(hostID string) (VolumeIO, error)

	mu     sync.Mutex
	active map[string]context.CancelFunc // migration ID → cancel
	// hooks are the commands confirmed for each active migration; the
	// hook phases run these, not what the meta file says by then.
	hooks map[string]*HookCommands

	// finishMu serialises a manual rollback against finalising the same
	// migration; purging holds the ones being finalised.
	finishMu sync.Mutex
	purging  map[string]bool
}

// Hosts is the part of host.Registry migrations use.
type Hosts interface {
	Pick(id string) (host.Host, error)
	List(ctx context.Context) ([]host.Info, error)
	DaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error)
}

// SetGlobalEnv makes migration deploys use the global variables like a
// manual deploy does.
func (s *Service) SetGlobalEnv(g deploy.GlobalVars) { s.globals = g }

// SetNotifier wires in the bell-icon notification center so each
// finished migration emits a success/failure notification.
func (s *Service) SetNotifier(n *notifications.Service) { s.notifs = n }

func NewService(db *sql.DB, hr Hosts, sm *stacks.Manager, ds *stacks.DeploymentStore) *Service {
	return &Service{
		store:       NewStore(db),
		hosts:       hr,
		stacks:      sm,
		deployments: ds,
		db:          db,
		active:      make(map[string]context.CancelFunc),
		hooks:       make(map[string]*HookCommands),
		purging:     make(map[string]bool),
	}
}

// Store exposes the underlying store for handler queries.
func (s *Service) Store() *Store { return s.store }

// Start settles the migrations a server restart interrupted. One that had
// not stopped the source yet ends failed with the stack still running
// there. From the stop on the stack may be down, or up on both hosts, so
// it is left rolling_back for Recover to restore on the source.
func (s *Service) Start(ctx context.Context) error {
	active, err := s.store.ListActive(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range active {
		if m.Status == StatusRollingBack {
			continue
		}
		if s.committed(ctx, m) {
			// Only the final status write was lost; the stack runs on the
			// target with the data it wrote there since.
			slog.Warn("migration committed before a server restart; recording it as completed",
				"id", m.ID, "stack", m.StackName)
			if err := s.store.updateStatusRetry(ctx, m.ID, StatusCompleted, "done", ""); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !stopsSource(m.Phase) {
			slog.Warn("migration interrupted by server restart before the source was stopped",
				"id", m.ID, "stack", m.StackName, "phase", m.Phase)
			if err := s.store.updateStatusRetry(ctx, m.ID, StatusFailed, m.Phase,
				"server restarted during the migration; the stack still runs on "+m.SourceHostID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		slog.Warn("migration interrupted by server restart; restoring the stack on the source",
			"id", m.ID, "stack", m.StackName, "phase", m.Phase)
		if err := s.store.updateStatusRetry(ctx, m.ID, StatusRollingBack, m.Phase,
			"server restarted during the migration; restoring the stack on "+m.SourceHostID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Recover restores the stacks of migrations Start left rolling_back on
// their source host, each once its hosts are back.
func (s *Service) Recover(ctx context.Context) {
	active, err := s.store.ListActive(ctx)
	if err != nil {
		slog.Error("migration recovery", "err", err)
		return
	}
	for _, m := range active {
		if m.Status == StatusRollingBack {
			// The phase doesn't tell where a manual rollback of a completed
			// migration stood; stopping a target that never ran the stack
			// does nothing.
			go s.rollback(ctx, m, true, "server restarted during the migration")
		}
	}
}

// committed reports whether migration m moved the stack's deployment to
// its target: completed, or in committing with the record already moved.
func (s *Service) committed(ctx context.Context, m *Migration) bool {
	if m.Phase == "done" {
		return true
	}
	if m.Phase != StatusCommitting || s.deployments == nil {
		return false
	}
	dep, err := s.deployments.Get(ctx, m.StackName)
	return err == nil && dep != nil && dep.HostID == m.TargetHostID
}

// stopsSource reports whether a migration in phase may have stopped the
// stack on the source.
func stopsSource(phase string) bool {
	switch phase {
	case StatusStopping, StatusSyncing:
		return true
	}
	return targetMayRun(phase)
}

// targetMayRun reports whether a migration in phase may have started the
// stack on the target.
func targetMayRun(phase string) bool {
	switch phase {
	case StatusStarting, StatusPostRestore, StatusHealthCheck, StatusCommitting:
		return true
	}
	return false
}

// Initiate starts a new migration. Returns the migration record.
// The actual work happens asynchronously in a goroutine. A stack with
// hook commands needs their digest in confirmHooks. confirm, when set,
// runs after every refusal, before anything is written.
func (s *Service) Initiate(ctx context.Context, stackName, targetHostID, userID, confirmHooks string, confirm func(context.Context) error) (*Migration, error) {
	// Validate stack exists.
	if _, err := s.stacks.Get(stackName); err != nil {
		return nil, fmt.Errorf("stack %q: %w", stackName, err)
	}

	// Look up source host from deployment table.
	dep, err := s.deployments.Get(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if dep == nil {
		return nil, fmt.Errorf("stack %q has no deployment — deploy it first", stackName)
	}
	sourceHostID := dep.HostID
	if sourceHostID == targetHostID {
		return nil, fmt.Errorf("source and target are the same host")
	}
	hooks, err := s.checkHooksConfirmed(stackName, confirmHooks)
	if err != nil {
		return nil, err
	}

	// Single-concurrency per stack.
	hasActive, err := s.store.HasActive(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if hasActive {
		return nil, fmt.Errorf("stack %q already has an active migration", stackName)
	}
	if confirm != nil {
		if err := confirm(ctx); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	m := &Migration{
		ID:           uuid.NewString(),
		StackName:    stackName,
		SourceHostID: sourceHostID,
		TargetHostID: targetHostID,
		Status:       StatusPending,
		StartedAt:    &now,
		InitiatedBy:  userID,
	}
	if err := s.store.Create(ctx, m); err != nil {
		return nil, err
	}

	// Launch the migration goroutine.
	mctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[m.ID] = cancel
	s.hooks[m.ID] = hooks
	s.mu.Unlock()
	go s.run(mctx, m.ID)

	return m, nil
}

// run is the migration goroutine. It advances through the 10 phases.
func (s *Service) run(ctx context.Context, migrationID string) {
	defer func() {
		s.mu.Lock()
		delete(s.active, migrationID)
		delete(s.hooks, migrationID)
		s.mu.Unlock()
	}()

	m, err := s.store.Get(ctx, migrationID)
	if err != nil {
		slog.Error("migration load failed", "id", migrationID, "err", err)
		return
	}

	phases := []struct {
		status string
		fn     func(context.Context, *Migration) error
	}{
		{StatusPreflight, s.phasePreflight},
		{StatusPreparing, s.phasePrepare},
		{StatusPreDump, s.phasePreDump},
		{StatusStopping, s.phaseStopping},
		{StatusSyncing, s.phaseSyncing},
		{StatusStarting, s.phaseStarting},
		{StatusPostRestore, s.phasePostRestore},
		{StatusHealthCheck, s.phaseHealthCheck},
		{StatusCommitting, s.phaseCommit},
	}

	for _, p := range phases {
		if ctx.Err() != nil {
			_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, p.status, "cancelled")
			return
		}
		_ = s.store.UpdateStatus(ctx, m.ID, p.status, p.status, "")
		m.Status = p.status
		m.Phase = p.status

		if err := p.fn(ctx, m); err != nil {
			slog.Warn("migration phase failed",
				"id", m.ID, "stack", m.StackName,
				"phase", p.status, "err", err)
			reason := "phase " + p.status + ": " + err.Error()
			m.progress().FailedPhase = p.status
			s.saveProgress(ctx, m)
			if stopsSource(p.status) {
				s.rollback(context.WithoutCancel(ctx), m, targetMayRun(p.status), reason)
			} else {
				_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, p.status, reason)
			}
			if s.notifs != nil {
				_, _ = s.notifs.Emit(context.Background(), notifications.EmitInput{
					Kind:     notifications.KindMigrationFail,
					Severity: notifications.SevError,
					Title:    "Stack migration failed: " + m.StackName,
					Body:     "Phase " + p.status + ": " + err.Error(),
					Link:     "/migrations",
					Perm:      rbac.PermStacksView,
					HostID:    m.SourceHostID,
					StackName: m.StackName,
				})
			}
			return
		}
	}

	if err := s.store.updateStatusRetry(ctx, m.ID, StatusCompleted, "done", ""); err != nil {
		slog.Error("migration completed, but the result could not be recorded", "id", m.ID, "err", err)
	}
	slog.Info("migration completed", "id", m.ID, "stack", m.StackName)
	if s.notifs != nil {
		_, _ = s.notifs.Emit(context.Background(), notifications.EmitInput{
			Kind:     notifications.KindMigrationOK,
			Severity: notifications.SevSuccess,
			Title:    "Stack migrated: " + m.StackName,
			Body:     "Moved from " + m.SourceHostID + " to " + m.TargetHostID,
			Link:     "/migrations",
			Perm:      rbac.PermStacksView,
			HostID:    m.TargetHostID,
			StackName: m.StackName,
		})
	}
}

// Phase stubs — each will be implemented in subsequent commits.

func (s *Service) phasePreflight(ctx context.Context, m *Migration) error {
	result, err := s.runPreflight(ctx, m)
	if err != nil {
		return err
	}
	if !result.Passed {
		var reasons []string
		for _, c := range result.Checks {
			if !c.Passed {
				reasons = append(reasons, c.Name+": "+c.Detail)
			}
		}
		return fmt.Errorf("preflight failed: %v", reasons)
	}
	return nil
}

// checkFiles refuses stack files changed outside Dockmesh after the
// migration was started; Initiate's caller confirms them up front.
func (s *Service) checkFiles(name string) error {
	changed, err := s.stacks.ModifiedOutside(name)
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		return fmt.Errorf("%w: %s", stacks.ErrModifiedOutside, strings.Join(changed, ", "))
	}
	return nil
}

// spec is the compose and env a migration deploys to target: the same
// globals and default override as a manual deploy.
func (s *Service) spec(ctx context.Context, name string, target host.Host) (deploy.Spec, error) {
	detail, err := s.stacks.Get(name)
	if err != nil {
		return deploy.Spec{}, err
	}
	dir, err := s.stacks.Dir(name)
	if err != nil {
		return deploy.Spec{}, err
	}
	spec, err := deploy.Resolve(ctx, s.globals, dir, name, detail.Compose, detail.Env, host.ResolvesStackPaths(target))
	if err != nil {
		return deploy.Spec{}, err
	}
	if err := s.stacks.CheckOverride(name, spec.Override); err != nil {
		return deploy.Spec{}, err
	}
	return spec, nil
}

func (s *Service) phasePrepare(ctx context.Context, m *Migration) error {
	slog.Info("migration prepare", "id", m.ID)
	if err := s.checkFiles(m.StackName); err != nil {
		return err
	}
	if _, err := s.spec(ctx, m.StackName, nil); err != nil {
		return err
	}
	detail, err := s.stacks.Get(m.StackName)
	if err != nil {
		return err
	}

	// Sync compose files to target agent (reuse P.7 stack.sync).
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}

	// Deploy stack on target will pull images automatically (compose
	// executor does image-pull-if-missing). We just need the compose
	// files on the target for the deploy in phaseStarting. The P.7
	// stack.sync frame handles that — but since the handler does it
	// fire-and-forget during deploy, here we need to be explicit.
	// For now we just verify images are available by listing them.
	dir, _ := s.stacks.Dir(m.StackName)
	proj, err := compose.LoadProject(ctx, dir, m.StackName, detail.Env)
	if err != nil {
		return fmt.Errorf("parse compose: %w", err)
	}

	// Count images to pull for progress.
	tgtImages, _ := target.ListImages(ctx, false)
	tgtSet := make(map[string]bool)
	for _, img := range tgtImages {
		for _, tag := range img.RepoTags {
			tgtSet[tag] = true
		}
	}
	var toPull []string
	for _, svc := range proj.Services {
		if svc.Image != "" && !tgtSet[svc.Image] {
			toPull = append(toPull, svc.Image)
		}
	}

	if len(toPull) > 0 {
		p := m.progress()
		p.ImagesTotal = len(toPull)
		_ = s.store.UpdateProgress(ctx, m.ID, p)
		slog.Info("migration prepare: pulling images on target",
			"id", m.ID, "count", len(toPull))
		// Images will be pulled by DeployStack in phaseStarting.
		// Just update progress for UI visibility.
		p.ImagesPulled = len(toPull)
		_ = s.store.UpdateProgress(ctx, m.ID, p)
	}

	return nil
}

func (s *Service) confirmedHooks(id string) *HookCommands {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks[id]
}

func (s *Service) phasePreDump(ctx context.Context, m *Migration) error {
	hooks := s.confirmedHooks(m.ID)
	if hooks == nil || hooks.PreDump == "" {
		slog.Info("migration pre-dump: no hooks configured", "id", m.ID)
		return nil
	}
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		return fmt.Errorf("source host: %w", err)
	}
	return s.executeHook(ctx, source, m.StackName, "pre_dump", hooks.PreDump)
}

func (s *Service) phaseStopping(ctx context.Context, m *Migration) error {
	slog.Info("migration stopping source", "id", m.ID)
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		return fmt.Errorf("source host: %w", err)
	}
	return source.StopStack(ctx, m.StackName)
}

// phaseSyncing copies the stack's volumes to the target while the stack
// is stopped on the source. The source's volumes are left as they are.
func (s *Service) phaseSyncing(ctx context.Context, m *Migration) error {
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		return fmt.Errorf("source host: %w", err)
	}
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	vols, err := s.volumePlan(ctx, m, source, target)
	if err != nil || len(vols) == 0 {
		return err
	}
	return s.copyVolumes(ctx, m, target, vols)
}

// namedVolumes returns the Docker volume names the stack's services use.
func (s *Service) namedVolumes(ctx context.Context, stackName string) ([]string, error) {
	detail, err := s.stacks.Get(stackName)
	if err != nil {
		return nil, err
	}
	dir, _ := s.stacks.Dir(stackName)
	proj, err := compose.LoadProject(ctx, dir, stackName, detail.Env)
	if err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}

	volumeSet := make(map[string]bool)
	for _, svc := range proj.Services {
		for _, v := range svc.Volumes {
			if v.Type == "volume" && v.Source != "" {
				// Resolve actual Docker volume name.
				actual := v.Source
				if vol, ok := proj.Volumes[v.Source]; ok {
					if vol.Name != "" {
						actual = vol.Name
					} else {
						actual = proj.Name + "_" + v.Source
					}
				}
				volumeSet[actual] = true
			}
		}
	}

	volumes := make([]string, 0, len(volumeSet))
	for v := range volumeSet {
		volumes = append(volumes, v)
	}
	sort.Strings(volumes)
	return volumes, nil
}

func (s *Service) phaseStarting(ctx context.Context, m *Migration) error {
	slog.Info("migration starting on target", "id", m.ID)
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	if err := s.checkFiles(m.StackName); err != nil {
		return err
	}
	spec, err := s.spec(ctx, m.StackName, target)
	if err != nil {
		return err
	}
	_, err = target.DeployStack(ctx, m.StackName, spec.Compose, spec.Env)
	return err
}

func (s *Service) phasePostRestore(ctx context.Context, m *Migration) error {
	hooks := s.confirmedHooks(m.ID)
	if hooks == nil || hooks.PostRestore == "" {
		slog.Info("migration post-restore: no hooks configured", "id", m.ID)
		return nil
	}
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	return s.executeHook(ctx, target, m.StackName, "post_restore", hooks.PostRestore)
}

func (s *Service) phaseHealthCheck(ctx context.Context, m *Migration) error {
	slog.Info("migration health-check", "id", m.ID)
	// Poll for 5 minutes: all containers must be running.
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := target.StackStatus(ctx, m.StackName)
		if err == nil && len(status) > 0 {
			allRunning := true
			for _, entry := range status {
				if entry.State != "running" {
					allRunning = false
					break
				}
			}
			if allRunning {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("containers did not reach running within 5 minutes")
}

func (s *Service) phaseCommit(ctx context.Context, m *Migration) error {
	slog.Info("migration commit", "id", m.ID)
	return s.deployments.Set(ctx, m.StackName, m.TargetHostID, "deployed")
}

// hostWait is how often rollback asks again for a host that is offline.
var hostWait = 5 * time.Second

// targetWaitMax bounds how long rollback waits for an offline target
// before it restarts the source anyway.
var targetWaitMax = 2 * time.Minute

// pickOnline returns host id, waiting while it is an agent that is not
// connected; max 0 waits until ctx ends. A host that is no longer
// registered ends the wait with host.ErrUnknownHost.
func (s *Service) pickOnline(ctx context.Context, id string, max time.Duration) (host.Host, error) {
	var deadline <-chan time.Time
	if max > 0 {
		deadline = time.After(max)
	}
	for {
		h, err := s.hosts.Pick(id)
		if !errors.Is(err, host.ErrAgentOffline) {
			return h, err
		}
		if known, lerr := s.hostKnown(ctx, id); lerr == nil && !known {
			return nil, host.ErrUnknownHost
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-deadline:
			return nil, err
		case <-time.After(hostWait):
		}
	}
}

func (s *Service) hostKnown(ctx context.Context, id string) (bool, error) {
	list, err := s.hosts.List(ctx)
	if err != nil {
		return false, err
	}
	for _, h := range list {
		if h.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// rollback restarts the source stack after a failed or completed
// migration, stopping the target first when it may run the stack. reason
// is why, kept in the record. While a host is offline the migration stays
// rolling_back.
func (s *Service) rollback(ctx context.Context, m *Migration, stopTarget bool, reason string) {
	slog.Warn("migration rollback", "id", m.ID, "stack", m.StackName)
	note := func(msg string) string {
		if reason == "" {
			return msg
		}
		return reason + "; " + msg
	}
	_ = s.store.UpdateStatus(ctx, m.ID, StatusRollingBack, m.Phase, note("restoring the stack on "+m.SourceHostID))
	source, err := s.pickOnline(ctx, m.SourceHostID, 0)
	if err != nil {
		slog.Error("rollback: source host unavailable", "err", err)
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback", note("source host unavailable: "+err.Error()))
		return
	}
	if err := s.checkFiles(m.StackName); err != nil {
		slog.Error("rollback: stack files changed outside Dockmesh", "err", err)
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback",
			note("source not restarted: "+err.Error()+"; review the files and deploy the stack on "+m.SourceHostID))
		return
	}
	spec, err := s.spec(ctx, m.StackName, source)
	if err != nil {
		slog.Error("rollback: stack read failed", "err", err)
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback", note("stack read failed: "+err.Error()))
		return
	}
	// The source's volumes stay on disk until purge-source, so a restart
	// re-attaches the pre-migration data.
	p := m.progress()
	committed := s.committed(ctx, m)
	// Checked before either host is touched: a deploy since then owns the
	// stack, and on the target it runs on the volumes copied there.
	if moved, err := s.deployedElsewhere(ctx, m, committed); err != nil || moved != "" {
		msg := "nothing undone: the stack was deployed on " + moved + " meanwhile"
		if err != nil {
			msg = "nothing undone: " + err.Error()
		}
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback", note(msg))
		return
	}
	if stopTarget || len(p.CreatedVolumes) > 0 {
		wait := targetWaitMax
		if !targetMayRun(m.Phase) && !committed && len(p.CreatedVolumes) == 0 {
			wait = hostWait
		}
		if target, terr := s.pickOnline(ctx, m.TargetHostID, wait); terr == nil {
			if stopTarget {
				if err := target.StopStack(ctx, m.StackName); err != nil {
					slog.Warn("rollback: stop target failed (proceeding anyway)", "err", err)
				} else {
					p.Undo = append(p.Undo, UndoStopTarget)
				}
			}
			switch {
			case len(p.CreatedVolumes) == 0:
			case committed:
				p.Undo = append(p.Undo, UndoKeepVolumes)
				reason = note("volumes " + strings.Join(p.CreatedVolumes, ", ") + " stay on " + m.TargetHostID + " with the data written there since the migration")
			default:
				if left := s.removeCreatedVolumes(ctx, m, target); len(left) > 0 {
					reason = note("remove volumes " + strings.Join(left, ", ") + " on " + m.TargetHostID + " by hand")
				} else {
					p.Undo = append(p.Undo, UndoRemoveVolumes)
				}
			}
			s.saveProgress(ctx, m)
		} else {
			slog.Warn("rollback: target host unavailable — source restart may race", "err", terr)
			var todo []string
			if targetMayRun(m.Phase) || committed {
				todo = append(todo, "stop the stack there")
			}
			if len(p.CreatedVolumes) > 0 && !committed {
				todo = append(todo, "remove volumes "+strings.Join(p.CreatedVolumes, ", "))
			}
			if len(todo) > 0 {
				reason = note("target " + m.TargetHostID + " was offline; once it is back, " + strings.Join(todo, " and "))
			}
		}
	}
	cleanLater := len(p.CreatedVolumes) > 0 && !committed
	// Again: waiting for the target gave a deploy time to move the stack.
	if moved, err := s.deployedElsewhere(ctx, m, committed); err != nil || moved != "" {
		msg := "source not restarted: the stack was deployed on " + moved + " meanwhile"
		if err != nil {
			msg = "source not restarted: " + err.Error()
		}
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback", note(msg))
		return
	}
	if _, err := source.DeployStack(ctx, m.StackName, spec.Compose, spec.Env); err != nil {
		slog.Error("rollback: restart source failed", "err", err)
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, "rollback", note("source restart failed: "+err.Error()))
		return
	}
	p.Undo = append(p.Undo, UndoRestartSource)
	s.saveProgress(ctx, m)
	// Reassert deployment ownership on source, clearing any lingering
	// target-side record from the completed migration.
	if s.deployments != nil {
		_ = s.deployments.Set(ctx, m.StackName, m.SourceHostID, "deployed")
	}
	final := note("stack restored on the source")
	if err := s.store.updateStatusRetry(ctx, m.ID, StatusRolledBack, "rollback", final); err != nil {
		slog.Error("rollback: stack restored on source, but the result could not be recorded", "migration", m.ID, "err", err)
	}
	if cleanLater {
		go s.cleanTargetLater(ctx, m, final)
	}
}

// lateCleanupMax bounds how long a rolled-back migration waits for its
// target to come back and take the volumes the rollback couldn't remove.
var lateCleanupMax = time.Hour

// cleanTargetLater removes the volumes a rollback left on the target once
// the target answers again, unless the stack was deployed anywhere but the
// source meanwhile.
func (s *Service) cleanTargetLater(ctx context.Context, m *Migration, msg string) {
	target, err := s.pickOnline(ctx, m.TargetHostID, lateCleanupMax)
	if err != nil {
		slog.Warn("migration: target still unavailable, its volumes stay", "id", m.ID, "volumes", m.progress().CreatedVolumes, "err", err)
		return
	}
	if moved, err := s.deployedElsewhere(ctx, m, false); err != nil || moved != "" {
		return
	}
	if left := s.removeCreatedVolumes(ctx, m, target); len(left) > 0 {
		s.saveProgress(ctx, m)
		return
	}
	p := m.progress()
	p.Undo = append(p.Undo, UndoRemoveVolumes)
	s.saveProgress(ctx, m)
	if err := s.store.updateStatusRetry(ctx, m.ID, StatusRolledBack, "rollback", msg+"; the volumes on "+m.TargetHostID+" were removed once it was back"); err != nil {
		slog.Warn("migration: target volumes removed, but the result could not be recorded", "id", m.ID, "err", err)
	}
}

// deployedElsewhere returns the host the stack is deployed on when a deploy
// since migration m moved it there: any host but the source, or but either
// end once m committed. Undoing m then would stop that deploy or run the
// stack twice.
func (s *Service) deployedElsewhere(ctx context.Context, m *Migration, committed bool) (string, error) {
	if s.deployments == nil {
		return "", nil
	}
	dep, err := s.deployments.Get(ctx, m.StackName)
	if err != nil || dep == nil {
		return "", err
	}
	if dep.HostID == m.SourceHostID || (committed && dep.HostID == m.TargetHostID) {
		return "", nil
	}
	return dep.HostID, nil
}

// Preflight runs the pre-flight checks without starting a migration.
// Used by the UI to show pass/fail before the user commits.
func (s *Service) Preflight(ctx context.Context, stackName, targetHostID string) (*PreflightResult, error) {
	dep, err := s.deployments.Get(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if dep == nil {
		return nil, fmt.Errorf("stack %q has no deployment", stackName)
	}
	m := &Migration{
		ID:           "preflight",
		StackName:    stackName,
		SourceHostID: dep.HostID,
		TargetHostID: targetHostID,
	}
	res, err := s.runPreflight(ctx, m)
	if err != nil {
		return nil, err
	}
	res.Hooks, err = s.HookCommands(stackName)
	if err != nil {
		res.Passed = false
		res.Checks = append(res.Checks, PreflightCheck{Name: "hooks", Status: CheckFail, Detail: err.Error()})
	}
	return res, nil
}

// ErrPurged refuses rolling back a migration whose source copy was
// removed: the source would start on empty volumes.
var ErrPurged = errors.New("the migration was finalised and its volumes on the source removed; it can't be rolled back")

// Rollback triggers a manual rollback for a completed migration.
func (s *Service) Rollback(ctx context.Context, migrationID string) error {
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	m, err := s.store.Get(ctx, migrationID)
	if err != nil {
		return err
	}
	if m.Status != StatusCompleted {
		return fmt.Errorf("can only rollback completed migrations (current: %s)", m.Status)
	}
	if s.purging[m.ID] || (m.Progress != nil && m.Progress.Purged) {
		return ErrPurged
	}
	claimed, err := s.store.claimRollback(ctx, m.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("migration %s is already being rolled back", m.ID)
	}
	go s.rollback(context.WithoutCancel(ctx), m, true, "")
	return nil
}
