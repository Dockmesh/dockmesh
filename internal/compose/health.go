package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrUnhealthy is returned when a container finishes its healthcheck
// startup window in the `unhealthy` state. The error wraps the last
// healthcheck output so operators see *why* it failed without digging
// through docker inspect.
var ErrUnhealthy = errors.New("container became unhealthy")

// ErrHealthTimeout is returned when the startup window elapses before
// the container reaches either healthy or unhealthy.
var ErrHealthTimeout = errors.New("healthcheck timeout")

// healthWaitMargin is the extra time we wait beyond the compose-spec
// start_period + (interval * retries). Docker's own health state
// machine can lag a beat behind the probe schedule, and a bit of slack
// prevents false timeouts on realistic infra.
const healthWaitMargin = 10 * time.Second

// fallbackWait is what WaitHealthy uses when the service has no
// healthcheck. We don't wait forever — just long enough to catch an
// obvious crash-loop (Exited within a few seconds of Start).
const fallbackWait = 5 * time.Second

// WaitHealthy blocks until the container reaches a healthy state (if
// a healthcheck is defined) or has been running for the fallback grace
// period (if not). Returns nil on healthy / still-running; ErrUnhealthy
// on explicit unhealthy; ErrHealthTimeout when the window elapses; or
// ctx.Err() on cancellation.
//
// Called after ContainerStart during Deploy / Scale so the next step
// (remove old replica, start next replica, …) only runs once this one
// is actually up. P.12.5a.
func WaitHealthy(ctx context.Context, cli *client.Client, containerID string, svc *composetypes.ServiceConfig) error {
	ctx, span := composeTracer.Start(ctx, "compose.wait_healthy",
		trace.WithAttributes(
			attribute.String("container.id", containerID),
		))
	defer span.End()

	deadline := time.Now().Add(healthWaitTimeout(svc))
	span.SetAttributes(attribute.String("deadline", deadline.Format(time.RFC3339)))

	// 500ms interval is fast enough to cut the wait in half for quick
	// services (nginx, redis) without slamming docker for slow ones
	// (postgres, elasticsearch).
	const pollInterval = 500 * time.Millisecond

	hasHealthcheck := svc != nil && svc.HealthCheck != nil && !svc.HealthCheck.Disable && len(svc.HealthCheck.Test) > 0

	for {
		if time.Now().After(deadline) {
			if !hasHealthcheck {
				// No healthcheck + container still running past the grace
				// period = good enough. Don't treat this as a failure.
				return nil
			}
			span.SetStatus(codes.Error, "timeout")
			return ErrHealthTimeout
		}

		insp, err := cli.ContainerInspect(ctx, containerID)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("inspect: %w", err)
		}

		if insp.State == nil {
			// Shouldn't happen in practice but keep us defensive.
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return err
			}
			continue
		}

		// Crash-loop detection regardless of healthcheck presence.
		if !insp.State.Running && insp.State.ExitCode != 0 {
			msg := fmt.Sprintf("container exited with code %d", insp.State.ExitCode)
			span.SetStatus(codes.Error, msg)
			return fmt.Errorf("%s", msg)
		}

		if !hasHealthcheck {
			// No healthcheck → wait out the fallback grace period to
			// catch immediate-exit crash-loops, then declare victory.
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return err
			}
			continue
		}

		if insp.State.Health == nil {
			// Healthcheck defined in compose but docker hasn't started
			// running it yet (spin-up race). Keep polling.
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return err
			}
			continue
		}

		switch insp.State.Health.Status {
		case "healthy":
			span.SetAttributes(attribute.String("health.status", "healthy"))
			return nil
		case "unhealthy":
			span.SetAttributes(attribute.String("health.status", "unhealthy"))
			last := lastHealthcheckOutput(insp.State.Health.Log)
			if last != "" {
				span.SetAttributes(attribute.String("health.output", last))
				return fmt.Errorf("%w: %s", ErrUnhealthy, last)
			}
			return ErrUnhealthy
		default:
			// "starting" — keep polling.
			if err := sleepCtx(ctx, pollInterval); err != nil {
				return err
			}
		}
	}
}

// healthWaitTimeout picks the outer bound for WaitHealthy. Honours
// compose start_period + interval*retries when defined; otherwise
// 60s is a pragmatic default that covers slow-starting images.
func healthWaitTimeout(svc *composetypes.ServiceConfig) time.Duration {
	if svc == nil || svc.HealthCheck == nil || svc.HealthCheck.Disable {
		return fallbackWait
	}
	hc := svc.HealthCheck
	if len(hc.Test) == 0 {
		return fallbackWait
	}

	var budget time.Duration
	if hc.StartPeriod != nil {
		budget += time.Duration(*hc.StartPeriod)
	}
	if hc.Interval != nil {
		retries := uint64(3)
		if hc.Retries != nil && *hc.Retries > 0 {
			retries = *hc.Retries
		}
		budget += time.Duration(*hc.Interval) * time.Duration(retries)
	}
	if budget <= 0 {
		budget = 60 * time.Second
	}
	return budget + healthWaitMargin
}

// lastHealthcheckOutput picks the most-recent probe output from the
// docker health log so we can surface it in the error message. Docker
// returns newest-last, so we take the tail.
func lastHealthcheckOutput(logs []*dtypes.HealthcheckResult) string {
	if len(logs) == 0 {
		return ""
	}
	out := strings.TrimSpace(logs[len(logs)-1].Output)
	if len(out) > 200 {
		out = out[:200] + "…"
	}
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// composeHealthToDocker maps the compose healthcheck spec to the docker
// API container.HealthConfig. Returns nil when the compose entry is nil
// or disabled — callers should not set HealthConfig on the container
// config in that case (docker inherits from the image).
func composeHealthToDocker(hc *composetypes.HealthCheckConfig) *container.HealthConfig {
	if hc == nil {
		return nil
	}
	if hc.Disable {
		// An explicit `disable: true` means "ignore the image's
		// HEALTHCHECK entirely" — docker treats `{"NONE"}` as the
		// disable sentinel.
		return &container.HealthConfig{Test: []string{"NONE"}}
	}
	if len(hc.Test) == 0 {
		return nil
	}
	out := &container.HealthConfig{
		Test: []string(hc.Test),
	}
	if hc.Interval != nil {
		out.Interval = time.Duration(*hc.Interval)
	}
	if hc.Timeout != nil {
		out.Timeout = time.Duration(*hc.Timeout)
	}
	if hc.StartPeriod != nil {
		out.StartPeriod = time.Duration(*hc.StartPeriod)
	}
	if hc.StartInterval != nil {
		out.StartInterval = time.Duration(*hc.StartInterval)
	}
	if hc.Retries != nil {
		out.Retries = int(*hc.Retries)
	}
	return out
}
