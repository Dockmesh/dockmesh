// Package updater handles one-click container image updates with
// rollback snapshots. The flow per update:
//
//  1. Pull the latest version of the container's image ref (for stack
//     containers: the image compose.yaml names for the service).
//  2. If the local image id is unchanged, return "already up to date".
//  3. Otherwise tag the *old* image as <repo>:<tag>-rollback-<unix> so it
//     survives the next prune and stays the rollback target.
//  4. Swap the container: the old one is renamed aside and stopped, the
//     new one is created under the original name, and the old one is only
//     removed once the new one runs. Volumes are carried over.
//  5. Record the change in update_history (and in the stack's deploy
//     history for stack containers), prune old rollback tags.
//
// Rollback runs the same swap with the stored rollback tag as the image.
package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

var (
	ErrDockerUnavailable = errors.New("docker unavailable")
	ErrAlreadyRolledBack = errors.New("already rolled back")
	ErrHistoryNotFound   = errors.New("history entry not found")
	ErrRollbackImageGone = errors.New("rollback image no longer exists (pruned or removed)")
	ErrBusy              = errors.New("an update of this container or stack is already running")
)

// Steps of an image update, in order, as reported to its operation.
const (
	StepPull        = "pull"
	StepRollbackTag = "rollback_tag"
	StepSwap        = "swap"
)

// UpdateSteps lists the steps Update reports.
var UpdateSteps = []operations.StepSpec{
	{Key: StepPull, Label: "Pull image"},
	{Key: StepRollbackTag, Label: "Keep current image for rollback"},
	{Key: StepSwap, Label: "Replace container and wait until it runs"},
}

// DefaultRollbackKeep is how many rollback tags per image ref survive
// a successful update.
const DefaultRollbackKeep = 3

// authResolver mirrors compose.authResolver — kept as an interface
// here so registries.Service can be wired in without an import cycle.
type authResolver interface {
	ResolveAuthForImage(ctx context.Context, image string, hostTags []string) (string, error)
}

type deployRecorder interface {
	RecordWithStatus(ctx context.Context, stackName, hostID, composeYAML, note, userID string, services []stacks.DeployHistoryService, success bool, durationMS int64, gitCommitSHA, errorMessage string) (int64, error)
}

type Service struct {
	docker       *docker.Client
	db           *sql.DB
	auth         authResolver
	stacks       *stacks.Manager
	history      deployRecorder
	rollbackKeep int

	mu   sync.Mutex
	busy map[string]bool
}

func NewService(dockerCli *docker.Client, db *sql.DB) *Service {
	return &Service{docker: dockerCli, db: db, rollbackKeep: DefaultRollbackKeep, busy: make(map[string]bool)}
}

// acquire serialises swaps per stack (or per standalone container), so
// two updates never rename and recreate the same replicas at once.
func (s *Service) acquire(info *container.InspectResponse, st *stackTarget) (func(), error) {
	key := "container:" + strings.TrimPrefix(info.Name, "/")
	if st != nil {
		key = "stack:" + st.stack
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[key] {
		return nil, ErrBusy
	}
	s.busy[key] = true
	return func() {
		s.mu.Lock()
		delete(s.busy, key)
		s.mu.Unlock()
	}, nil
}

// ManagedStack returns the stack a container belongs to when that stack
// is managed here (and an update would rebuild it from compose.yaml),
// or "" otherwise.
func (s *Service) ManagedStack(ctx context.Context, containerID string) string {
	if s.docker == nil || s.stacks == nil {
		return ""
	}
	info, err := s.docker.Raw().ContainerInspect(ctx, containerID)
	if err != nil || info.Config == nil {
		return ""
	}
	stack := info.Config.Labels[compose.LabelProject]
	if stack == "" || info.Config.Labels[compose.LabelService] == "" || !s.stacks.Has(stack) {
		return ""
	}
	return stack
}

// SetAuthResolver wires in registry-credential resolution so the
// single-container "Update" button can pull from authenticated
// registries (GHCR, ECR, private Hub repos, etc.) Nil = anonymous pulls.
func (s *Service) SetAuthResolver(r authResolver) { s.auth = r }

// SetStacks makes updates of stack containers go through compose.yaml
// and land in the stack's deploy history. Without it every container is
// treated as standalone.
func (s *Service) SetStacks(m *stacks.Manager, h deployRecorder) {
	s.stacks = m
	s.history = h
}

// SetRollbackKeep sets how many rollback tags per image ref are kept;
// values below 1 keep the default.
func (s *Service) SetRollbackKeep(n int) {
	if n >= 1 {
		s.rollbackKeep = n
	}
}

func (s *Service) resolveImageAuth(ctx context.Context, image string) string {
	if s.auth == nil {
		return ""
	}
	auth, err := s.auth.ResolveAuthForImage(ctx, image, nil)
	if err != nil {
		return ""
	}
	return auth
}

type Result struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	OldDigest     string `json:"old_digest"`
	NewDigest     string `json:"new_digest"`
	Updated       bool   `json:"updated"`
	RollbackTag   string `json:"rollback_tag,omitempty"`
	HistoryID     int64  `json:"history_id,omitempty"`
	// Stack is set when the container belongs to a managed stack and
	// was rebuilt from its compose.yaml.
	Stack string `json:"stack,omitempty"`
}

type Entry struct {
	ID            int64      `json:"id"`
	ContainerName string     `json:"container_name"`
	ImageRef      string     `json:"image_ref"`
	OldDigest     string     `json:"old_digest"`
	NewDigest     string     `json:"new_digest"`
	RollbackTag   string     `json:"rollback_tag"`
	AppliedAt     time.Time  `json:"applied_at"`
	RolledBackAt  *time.Time `json:"rolled_back_at,omitempty"`
}

// Update pulls the latest image for the container and, if it differs,
// snapshots the old image and replaces the container. A container that
// belongs to a Dockmesh stack is rebuilt from the stack's compose.yaml
// (every replica of its service), so the stack stays the source of truth.
func (s *Service) Update(ctx context.Context, containerID, userID string) (*Result, error) {
	if s.docker == nil {
		return nil, ErrDockerUnavailable
	}
	cli := s.docker.Raw()

	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	st, err := s.stackTarget(ctx, &info)
	if err != nil {
		return nil, err
	}
	image := baseRef(info.Config.Image)
	if st != nil {
		image = st.svc.Image
	}
	ref := normalizeImageRef(image)
	containerName := strings.TrimPrefix(info.Name, "/")
	oldImageID := info.Image
	release, err := s.acquire(&info, st)
	if err != nil {
		return nil, err
	}
	defer release()

	op := operations.FromContext(ctx)
	authBlob := s.resolveImageAuth(ctx, ref)
	secrets := authSecrets(authBlob)
	op.AddSecrets(secrets...)
	op.StepStart(StepPull, "")
	op.Logf("info", "pulling %s", ref)
	if err := pull(ctx, cli, ref, authBlob, secrets); err != nil {
		op.StepFail(StepPull, err)
		return nil, err
	}

	newInfo, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		err = fmt.Errorf("inspect new image: %w", err)
		op.StepFail(StepPull, err)
		return nil, err
	}
	newImageID := newInfo.ID
	op.StepDone(StepPull, shortID(newImageID))

	res := &Result{
		ContainerID:   containerID,
		ContainerName: containerName,
		Image:         ref,
		OldDigest:     oldImageID,
		NewDigest:     newImageID,
	}
	if st != nil {
		res.Stack = st.stack
	}
	if oldImageID == newImageID {
		op.Logf("info", "%s is already up to date", containerName)
		op.StepSkip(StepRollbackTag, "already up to date")
		op.StepSkip(StepSwap, "already up to date")
		return res, nil
	}

	// Tagged before the swap so it is also there when the swap fails.
	rollbackTag := sanitizeTag(ref, time.Now().Unix())
	op.StepStart(StepRollbackTag, "")
	if err := cli.ImageTag(ctx, oldImageID, rollbackTag); err != nil {
		err = fmt.Errorf("tag rollback image: %w", err)
		op.StepFail(StepRollbackTag, err)
		return nil, err
	}
	op.StepDone(StepRollbackTag, rollbackTag)
	op.StepStart(StepSwap, "")

	var newContainerID string
	if st != nil {
		newContainerID, err = s.swapService(ctx, cli, st, containerName, image, rollbackTag)
	} else {
		health := info.Config.Healthcheck
		if health == nil && newInfo.Config != nil {
			health = newInfo.Config.Healthcheck
		}
		newContainerID, err = swapContainer(ctx, cli, &info, standaloneSpec(&info, ref, health))
	}
	if err != nil {
		err = fmt.Errorf("update %s: %w (previous image kept as %s)", containerName, err, rollbackTag)
		op.StepFail(StepSwap, err)
		return nil, err
	}
	op.StepDone(StepSwap, shortID(newContainerID))

	exec, err := s.db.ExecContext(ctx, `
		INSERT INTO update_history (container_name, image_ref, old_digest, new_digest, rollback_tag)
		VALUES (?, ?, ?, ?, ?)
	`, containerName, ref, oldImageID, newImageID, rollbackTag)
	if err != nil {
		slog.Warn("update history insert failed", "err", err)
	} else if id, err := exec.LastInsertId(); err == nil {
		res.HistoryID = id
	}
	if st != nil {
		digest := imageDigest(&newInfo, ref)
		note := fmt.Sprintf("image update: service %s, %s now resolves to %s (was %s)",
			st.svc.Name, image, digest, shortID(oldImageID))
		s.recordStack(ctx, st, userID, note, image+"@"+digest)
	}
	s.pruneRollbackTags(ctx, cli, ref)

	res.ContainerID = newContainerID
	res.RollbackTag = rollbackTag
	res.Updated = true
	return res, nil
}

// pull pulls ref and reads the progress stream to its end. Only the pull
// can be cancelled: nothing has changed yet, and the container keeps
// running on its current image.
func pull(ctx context.Context, cli *client.Client, ref, authBlob string, secrets []string) error {
	op := operations.FromContext(ctx)
	pctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	op.AllowCancel(stop)
	defer op.DisallowCancel(pctx)
	rc, err := cli.ImagePull(pctx, ref, imagetypes.PullOptions{RegistryAuth: authBlob})
	if err == nil {
		// Ends a read of the progress stream that blocks on the registry.
		defer context.AfterFunc(pctx, func() { _ = rc.Close() })()
		err = drainPull(rc, op, secrets)
		rc.Close()
	}
	if c := context.Cause(pctx); pctx.Err() != nil && errors.Is(c, operations.ErrCancelled) {
		err = c
	}
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// Rollback reverts the container to the image stored in the given history
// entry, which must belong to that container. Stack containers are
// rebuilt from compose.yaml with only the image swapped for the rollback
// tag.
func (s *Service) Rollback(ctx context.Context, containerID string, historyID int64, userID string) (*Result, error) {
	if s.docker == nil {
		return nil, ErrDockerUnavailable
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	var e Entry
	var rolledAt sql.NullTime
	err = s.db.QueryRowContext(ctx, `
		SELECT id, container_name, image_ref, old_digest, new_digest, rollback_tag, applied_at, rolled_back_at
		FROM update_history WHERE id = ? AND container_name = ?`, historyID, strings.TrimPrefix(info.Name, "/")).
		Scan(&e.ID, &e.ContainerName, &e.ImageRef, &e.OldDigest, &e.NewDigest, &e.RollbackTag, &e.AppliedAt, &rolledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, err
	}
	if rolledAt.Valid {
		return nil, ErrAlreadyRolledBack
	}

	if _, err := cli.ImageInspect(ctx, e.RollbackTag); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, ErrRollbackImageGone
		}
		return nil, fmt.Errorf("inspect rollback image: %w", err)
	}

	st, err := s.stackTarget(ctx, &info)
	if err != nil {
		return nil, err
	}
	release, err := s.acquire(&info, st)
	if err != nil {
		return nil, err
	}
	defer release()

	var newID string
	if st != nil {
		newID, err = s.swapService(ctx, cli, st, e.ContainerName, e.RollbackTag, info.Image)
	} else {
		newID, err = swapContainer(ctx, cli, &info, standaloneSpec(&info, e.RollbackTag, info.Config.Healthcheck))
	}
	if err != nil {
		return nil, fmt.Errorf("rollback %s: %w", e.ContainerName, err)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE update_history SET rolled_back_at = CURRENT_TIMESTAMP WHERE id = ?`, historyID); err != nil {
		slog.Warn("update history rollback update failed", "err", err)
	}
	res := &Result{
		ContainerID:   newID,
		ContainerName: e.ContainerName,
		Image:         e.RollbackTag,
		OldDigest:     e.NewDigest,
		NewDigest:     e.OldDigest,
		Updated:       true,
		HistoryID:     historyID,
	}
	if st != nil {
		res.Stack = st.stack
		note := fmt.Sprintf("image rollback: service %s runs %s; compose.yaml still names %s, so the next deploy uses that again",
			st.svc.Name, e.RollbackTag, st.svc.Image)
		s.recordStack(ctx, st, userID, note, e.RollbackTag)
	}
	return res, nil
}

// History returns the update history for a single container, newest first.
func (s *Service) History(ctx context.Context, containerName string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, container_name, image_ref, old_digest, new_digest, rollback_tag, applied_at, rolled_back_at
		FROM update_history
		WHERE container_name = ?
		ORDER BY id DESC
		LIMIT 50
	`, containerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var rolledAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.ContainerName, &e.ImageRef, &e.OldDigest, &e.NewDigest, &e.RollbackTag, &e.AppliedAt, &rolledAt); err != nil {
			return nil, err
		}
		if rolledAt.Valid {
			t := rolledAt.Time
			e.RolledBackAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// stackTarget is a container that belongs to a stack this server manages.
type stackTarget struct {
	stack       string
	composeYAML string
	proj        *composetypes.Project
	svc         *composetypes.ServiceConfig
}

// stackTarget returns nil for containers outside any managed stack
// (plain `docker run`, compose projects Dockmesh doesn't own).
func (s *Service) stackTarget(ctx context.Context, info *container.InspectResponse) (*stackTarget, error) {
	if s.stacks == nil || info.Config == nil {
		return nil, nil
	}
	stack := info.Config.Labels[compose.LabelProject]
	service := info.Config.Labels[compose.LabelService]
	if stack == "" || service == "" || !s.stacks.Has(stack) {
		return nil, nil
	}
	dir, err := s.stacks.Dir(stack)
	if err != nil {
		return nil, err
	}
	detail, err := s.stacks.Get(stack)
	if err != nil {
		return nil, fmt.Errorf("load stack %s: %w", stack, err)
	}
	proj, err := compose.LoadProject(ctx, dir, stack, detail.Env)
	if err != nil {
		return nil, fmt.Errorf("load stack %s: %w", stack, err)
	}
	svc, ok := proj.Services[service]
	if !ok {
		return nil, fmt.Errorf("service %s is no longer in stack %s; redeploy the stack instead", service, stack)
	}
	if svc.Image == "" {
		return nil, fmt.Errorf("service %s in stack %s has no image", service, stack)
	}
	return &stackTarget{stack: stack, composeYAML: detail.Compose, proj: proj, svc: &svc}, nil
}

// swapService moves every container of the stack service onto image,
// one at a time. If one fails, the replicas already moved are put back
// on fallback so the service doesn't end up split across two images.
// Returns the new ID of the container called name.
func (s *Service) swapService(ctx context.Context, cli *client.Client, st *stackTarget, name, image, fallback string) (string, error) {
	cs, err := compose.ServiceContainers(ctx, cli, st.stack, st.svc.Name)
	if err != nil {
		return "", err
	}
	var done []string
	var picked string
	for _, c := range cs {
		// A container parked by an earlier swap that could not be
		// removed still carries the service labels.
		if strings.Contains(containerNameOf(c), "-dmold-") {
			continue
		}
		op := operations.FromContext(ctx)
		op.Logf("info", "replacing %s", containerNameOf(c))
		id, err := s.swapReplica(ctx, cli, st, c.ID, image)
		if err != nil {
			op.Logf("error", "%s: %s", containerNameOf(c), err)
			for _, d := range done {
				op.Logf("warn", "putting %s back on the previous image", shortID(d))
				if _, rerr := s.swapReplica(context.WithoutCancel(ctx), cli, st, d, fallback); rerr != nil {
					slog.Error("updater: put replica back failed", "stack", st.stack, "container", d, "err", rerr)
				}
			}
			return "", err
		}
		done = append(done, id)
		if containerNameOf(c) == name {
			picked = id
		}
	}
	if picked == "" && len(done) > 0 {
		picked = done[0]
	}
	return picked, nil
}

func (s *Service) swapReplica(ctx context.Context, cli *client.Client, st *stackTarget, containerID, image string) (string, error) {
	old, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect: %w", err)
	}
	cfg, hostCfg, netCfg, svc, err := compose.ServiceContainerConfig(st.proj, st.svc.Name)
	if err != nil {
		return "", err
	}
	cfg.Image = image
	return swapContainer(ctx, cli, &old, newSpec{cfg: cfg, hostCfg: hostCfg, netCfg: netCfg, health: svc})
}

// recordStack adds a deploy-history row so the stack's history shows
// that its containers now run a different image than the last deploy.
func (s *Service) recordStack(ctx context.Context, st *stackTarget, userID, note, image string) {
	if s.history == nil {
		return
	}
	services := []stacks.DeployHistoryService{{Service: st.svc.Name, Image: image}}
	if _, err := s.history.RecordWithStatus(ctx, st.stack, "local", st.composeYAML, note, userID, services, true, 0, "", ""); err != nil {
		slog.Warn("updater: record stack history failed", "stack", st.stack, "err", err)
	}
}

// standaloneSpec rebuilds a non-stack container from its own inspected
// config with a new image.
func standaloneSpec(info *container.InspectResponse, image string, health *container.HealthConfig) newSpec {
	cfg := *info.Config
	cfg.Image = image
	if info.Config.Volumes != nil {
		cfg.Volumes = make(map[string]struct{}, len(info.Config.Volumes))
		for k, v := range info.Config.Volumes {
			cfg.Volumes[k] = v
		}
	}
	hostCfg := *info.HostConfig
	hostCfg.Mounts = append([]mount.Mount(nil), info.HostConfig.Mounts...)

	netCfg := &dnetwork.NetworkingConfig{}
	if info.NetworkSettings != nil && len(info.NetworkSettings.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*dnetwork.EndpointSettings, len(info.NetworkSettings.Networks))
		short := shortID(info.ID)
		for netName, ep := range info.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			// Docker adds the container's short ID as an alias; the new
			// container gets its own.
			var aliases []string
			for _, a := range ep.Aliases {
				if a != short {
					aliases = append(aliases, a)
				}
			}
			netCfg.EndpointsConfig[netName] = &dnetwork.EndpointSettings{
				IPAMConfig: ep.IPAMConfig,
				Links:      ep.Links,
				Aliases:    aliases,
				DriverOpts: ep.DriverOpts,
			}
		}
	}
	return newSpec{cfg: &cfg, hostCfg: &hostCfg, netCfg: netCfg, health: healthFromConfig(health)}
}

func containerNameOf(c container.Summary) string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func normalizeImageRef(ref string) string {
	// If the ref has no tag, docker treats it as :latest.
	// Only check the final path segment so `localhost:5000/foo` doesn't
	// get misread as `localhost` + tag `5000/foo`.
	lastSlash := strings.LastIndex(ref, "/")
	tagPart := ref[lastSlash+1:]
	if !strings.Contains(tagPart, ":") {
		return ref + ":latest"
	}
	return ref
}

// sanitizeTag builds a rollback tag in the form <repo>:<original-tag>-rollback-<ts>.
func sanitizeTag(ref string, ts int64) string {
	return fmt.Sprintf("%s%d", rollbackPrefix(ref), ts)
}

// rollbackPrefix is the part of every rollback tag for ref before the
// timestamp.
func rollbackPrefix(ref string) string {
	lastSlash := strings.LastIndex(ref, "/")
	tagPart := ref[lastSlash+1:]
	colon := strings.LastIndex(tagPart, ":")
	if colon < 0 {
		return ref + ":rollback-"
	}
	repo := ref[:lastSlash+1+colon]
	tag := tagPart[colon+1:]
	return fmt.Sprintf("%s:%s-rollback-", repo, tag)
}
