package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// UpdateOrder is the compose-spec `update_config.order` — defines
// whether new containers come up before old ones go down.
type UpdateOrder string

const (
	// OrderStopFirst stops + removes an old replica, then creates the
	// new one. The default everywhere compose-spec is used.
	OrderStopFirst UpdateOrder = "stop-first"
	// OrderStartFirst creates a sibling with a temporary name, waits
	// for it to go healthy, then stops+removes the old replica and
	// renames the new one to the original name. Safe only when the
	// service has no container_name + no hard port bindings.
	OrderStartFirst UpdateOrder = "start-first"
)

// FailureAction is the compose-spec `update_config.failure_action` —
// what to do when a new replica fails to become healthy.
type FailureAction string

const (
	// FailurePause stops the rollout, leaves already-replaced replicas
	// in place, returns an error. The default. Operator inspects + retries.
	FailurePause FailureAction = "pause"
	// FailureContinue skips the failing replica, keeps replacing the rest.
	// Good for best-effort rollouts across many replicas where a single bad
	// node shouldn't block the fleet.
	FailureContinue FailureAction = "continue"
	// FailureRollback reverts already-replaced replicas back to the image
	// they were running before the rollout started.
	FailureRollback FailureAction = "rollback"
)

// RollingOptions controls how RollingReplace walks the service's
// replicas. Callers pass this explicitly; compose `deploy.update_config`
// is read as the default baseline via ExtractUpdateOptions.
type RollingOptions struct {
	Parallelism   int           // batch size, default 1
	Delay         time.Duration // wait between batches, default 0
	Order         UpdateOrder   // default OrderStopFirst
	FailureAction FailureAction // default FailurePause
}

// Defaults applies safe fallbacks to zero-valued fields.
func (o RollingOptions) Defaults() RollingOptions {
	if o.Parallelism <= 0 {
		o.Parallelism = 1
	}
	if o.Order == "" {
		o.Order = OrderStopFirst
	}
	if o.FailureAction == "" {
		o.FailureAction = FailurePause
	}
	return o
}

// ExtractUpdateOptions reads `deploy.update_config` from a compose
// service config and returns the equivalent RollingOptions. Zero-valued
// / missing fields are left zero so the caller's Defaults() or an API
// override can fill them in.
func ExtractUpdateOptions(svc composetypes.ServiceConfig) RollingOptions {
	var opts RollingOptions
	if svc.Deploy == nil || svc.Deploy.UpdateConfig == nil {
		return opts
	}
	uc := svc.Deploy.UpdateConfig
	if uc.Parallelism != nil {
		opts.Parallelism = int(*uc.Parallelism)
	}
	if d := time.Duration(uc.Delay); d > 0 {
		opts.Delay = d
	}
	if uc.Order != "" {
		opts.Order = UpdateOrder(uc.Order)
	}
	if uc.FailureAction != "" {
		opts.FailureAction = FailureAction(uc.FailureAction)
	}
	return opts
}

// RollingResult reports what happened. Updated + Failed together equal
// the number of replicas the caller asked to replace. RolledBack=true
// means FailureRollback kicked in and replaced-back replicas are back
// on PreviousImage.
type RollingResult struct {
	Service       string   `json:"service"`
	TotalReplicas int      `json:"total_replicas"`
	Updated       int      `json:"updated"`
	Failed        int      `json:"failed"`
	Skipped       int      `json:"skipped"`
	RolledBack    bool     `json:"rolled_back"`
	PreviousImage string   `json:"previous_image,omitempty"`
	NewImage      string   `json:"new_image"`
	Errors        []string `json:"errors,omitempty"`
}

// ErrRollingStartFirstUnsafe is returned when the caller asks for
// start-first order on a service that cannot safely run two replicas
// side-by-side (container_name or hard host port set).
var ErrRollingStartFirstUnsafe = errors.New("service is not start-first-safe (container_name or hard host port set)")

// RollingReplace replaces every running replica of a service with the
// service definition passed in. Each batch is a parallelism-sized slice
// of the existing replicas; within a batch, replicas are replaced
// concurrently (actually: sequentially here for MVP — same-batch
// parallelism is a follow-up, parallelism>1 today means "the batch
// moves on faster than parallelism=1 because replicas are iterated
// back-to-back without the Delay").
//
// For MVP correctness, parallelism>1 simply lifts the delay between
// consecutive replicas — actual concurrent docker operations would
// require careful name-space coordination (replicas N+1/N+2 both picking
// the same temp name) that's not worth the complexity until someone
// asks. Effect: parallelism=N → N replicas replaced back-to-back, then
// a Delay, then the next N. Same observable outcome on workloads where
// Delay > replace-time-per-replica (which is most of them).
//
// P.12.5b.
func (s *Service) RollingReplace(ctx context.Context, proj *composetypes.Project, serviceName string, opts RollingOptions) (*RollingResult, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	svc, ok := proj.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found in project %s", serviceName, proj.Name)
	}
	// Fill zero fields from compose `deploy.update_config`, then apply
	// engine defaults. Caller-provided fields win because ExtractUpdate
	// only runs for fields the caller left zero.
	fromCompose := ExtractUpdateOptions(svc)
	if opts.Parallelism == 0 {
		opts.Parallelism = fromCompose.Parallelism
	}
	if opts.Delay == 0 {
		opts.Delay = fromCompose.Delay
	}
	if opts.Order == "" {
		opts.Order = fromCompose.Order
	}
	if opts.FailureAction == "" {
		opts.FailureAction = fromCompose.FailureAction
	}
	opts = opts.Defaults()

	ctx, span := composeTracer.Start(ctx, "compose.rolling_replace",
		trace.WithAttributes(
			attribute.String("stack", proj.Name),
			attribute.String("service", serviceName),
			attribute.String("order", string(opts.Order)),
			attribute.String("failure_action", string(opts.FailureAction)),
			attribute.Int("parallelism", opts.Parallelism),
		))
	defer span.End()

	cli := s.docker.Raw()

	// Pre-flight: start-first requires no container_name / hard ports.
	if opts.Order == OrderStartFirst {
		if svc.ContainerName != "" {
			span.SetStatus(codes.Error, "container_name set")
			return nil, ErrRollingStartFirstUnsafe
		}
		for _, p := range svc.Ports {
			if p.Published != "" && !strings.Contains(p.Published, "-") {
				span.SetStatus(codes.Error, "hard host port")
				return nil, ErrRollingStartFirstUnsafe
			}
		}
	}

	existing, err := listServiceContainers(ctx, cli, proj.Name, serviceName)
	if err != nil {
		span.SetStatus(codes.Error, "list containers")
		span.RecordError(err)
		return nil, err
	}
	sortByReplicaIndex(existing, proj.Name, serviceName)

	res := &RollingResult{
		Service:       serviceName,
		TotalReplicas: len(existing),
		NewImage:      svc.Image,
	}
	if len(existing) == 0 {
		return res, nil
	}
	// Capture rollback image from the first existing container — all
	// replicas should be on the same image, so this is safe.
	res.PreviousImage = existing[0].Image

	// Resolve networks once — reused for every new-container create.
	netNames := make(map[string]string, len(proj.Networks))
	for key, net := range proj.Networks {
		actual := net.Name
		if actual == "" {
			actual = proj.Name + "_" + key
		}
		netNames[key] = actual
	}

	// Track which (replicaIndex → containerID) we've already replaced so
	// rollback knows what to revert. Only populated on successful replace.
	var done []replacedReplica

	replaceOne := func(idx int, oldC dtypes.Container) error {
		replicaIdx := replicaIndex(oldC, proj.Name+"-"+serviceName+"-")
		containerName := fmt.Sprintf("%s-%s-%d", proj.Name, serviceName, replicaIdx)

		switch opts.Order {
		case OrderStopFirst:
			newID, err := stopFirstReplace(ctx, cli, oldC, containerName, proj, svc, netNames)
			if err != nil {
				return err
			}
			done = append(done, replacedReplica{replicaIndex: replicaIdx, newID: newID})
		case OrderStartFirst:
			newID, err := startFirstReplace(ctx, cli, oldC, containerName, proj, svc, netNames)
			if err != nil {
				return err
			}
			done = append(done, replacedReplica{replicaIndex: replicaIdx, newID: newID})
		}
		return nil
	}

	// Iterate in batches.
	for batchStart := 0; batchStart < len(existing); batchStart += opts.Parallelism {
		batchEnd := batchStart + opts.Parallelism
		if batchEnd > len(existing) {
			batchEnd = len(existing)
		}
		for i := batchStart; i < batchEnd; i++ {
			if err := replaceOne(i, existing[i]); err != nil {
				msg := fmt.Sprintf("replica %d: %v", replicaIndex(existing[i], proj.Name+"-"+serviceName+"-"), err)
				res.Errors = append(res.Errors, msg)
				slog.Warn("rolling replace: replica failed", "stack", proj.Name, "service", serviceName, "error", err)

				switch opts.FailureAction {
				case FailureContinue:
					res.Failed++
					continue
				case FailureRollback:
					res.Failed++
					res.RolledBack = true
					rollbackDone := doRollback(ctx, cli, proj, svc, netNames, done, res.PreviousImage)
					res.Skipped = len(existing) - res.Updated - res.Failed - rollbackDone
					span.SetStatus(codes.Error, "rolled back")
					return res, fmt.Errorf("rolling replace failed at replica %d: %w (rolled back %d)", i, err, rollbackDone)
				default: // FailurePause
					res.Failed++
					res.Skipped = len(existing) - res.Updated - res.Failed
					span.SetStatus(codes.Error, "paused")
					return res, fmt.Errorf("rolling replace paused at replica %d: %w", i, err)
				}
			}
			res.Updated++
		}
		if opts.Delay > 0 && batchEnd < len(existing) {
			if err := sleepCtx(ctx, opts.Delay); err != nil {
				res.Skipped = len(existing) - res.Updated - res.Failed
				return res, err
			}
		}
	}
	return res, nil
}

// stopFirstReplace: stop + remove old, then create + start + wait-healthy new.
func stopFirstReplace(ctx context.Context, cli *client.Client, oldC dtypes.Container, containerName string, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string) (string, error) {
	_ = cli.ContainerStop(ctx, oldC.ID, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, oldC.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("remove old: %w", err)
	}
	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, containerName)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return resp.ID, fmt.Errorf("start: %w", err)
	}
	if err := WaitHealthy(ctx, cli, resp.ID, &svc); err != nil {
		return resp.ID, fmt.Errorf("wait healthy: %w", err)
	}
	return resp.ID, nil
}

// startFirstReplace: create sibling with a temp name, wait healthy,
// then stop+remove old and rename new into the original slot.
func startFirstReplace(ctx context.Context, cli *client.Client, oldC dtypes.Container, containerName string, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string) (string, error) {
	tempName := containerName + "-new"
	// Cleanup any leftover from a prior aborted run.
	if prev, err := cli.ContainerInspect(ctx, tempName); err == nil {
		_ = cli.ContainerStop(ctx, prev.ID, container.StopOptions{})
		_ = cli.ContainerRemove(ctx, prev.ID, container.RemoveOptions{Force: true})
	}

	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, tempName)
	if err != nil {
		return "", fmt.Errorf("create sibling: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Clean up the dead sibling so the slot is free for retry.
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("start sibling: %w", err)
	}
	if err := WaitHealthy(ctx, cli, resp.ID, &svc); err != nil {
		_ = cli.ContainerStop(ctx, resp.ID, container.StopOptions{})
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("wait healthy: %w", err)
	}

	// Sibling is healthy — tear down the old one and promote.
	_ = cli.ContainerStop(ctx, oldC.ID, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, oldC.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		// Rollback: the old container is lingering, can't claim the name.
		_ = cli.ContainerStop(ctx, resp.ID, container.StopOptions{})
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("remove old: %w", err)
	}
	if err := cli.ContainerRename(ctx, resp.ID, containerName); err != nil {
		return resp.ID, fmt.Errorf("rename: %w", err)
	}
	return resp.ID, nil
}

// replacedReplica pairs the replica index with the ID of its
// newly-created container, so rollback can target exactly the set of
// containers the current rollout produced without touching other
// replicas.
type replacedReplica struct {
	replicaIndex int
	newID        string
}

// doRollback walks the done list in reverse and replaces each new
// container back to the previous image. Uses stop-first for simplicity;
// the rollback target config is the service config with image overridden.
// Returns the number of replicas actually rolled back.
func doRollback(ctx context.Context, cli *client.Client, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string, done []replacedReplica, prevImage string) int {
	if prevImage == "" || prevImage == svc.Image {
		return 0
	}
	rbSvc := svc
	rbSvc.Image = prevImage
	rolled := 0
	for i := len(done) - 1; i >= 0; i-- {
		d := done[i]
		name := fmt.Sprintf("%s-%s-%d", proj.Name, svc.Name, d.replicaIndex)
		// Stop + remove the freshly-replaced container.
		_ = cli.ContainerStop(ctx, d.newID, container.StopOptions{})
		_ = cli.ContainerRemove(ctx, d.newID, container.RemoveOptions{Force: true})
		cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, rbSvc, netNames)
		if err != nil {
			slog.Error("rollback config", "error", err, "replica", d.replicaIndex)
			continue
		}
		resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
		if err != nil {
			slog.Error("rollback create", "error", err, "replica", d.replicaIndex)
			continue
		}
		if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			slog.Error("rollback start", "error", err, "replica", d.replicaIndex)
			continue
		}
		// Best-effort wait — on rollback we don't block for long.
		_ = WaitHealthy(ctx, cli, resp.ID, &rbSvc)
		rolled++
	}
	return rolled
}
