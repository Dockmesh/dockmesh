package updater

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notifications"
	dtypes "github.com/docker/docker/api/types"
)

// UpdateInfo describes whether a newer image is available for one image
// reference and, when known, what the latest tag/digest looks like. It's
// the value side of the watcher's cache and the wire format of
// GET /api/v1/images/updates.
type UpdateInfo struct {
	Image           string    `json:"image"`
	UpdateAvailable bool      `json:"update_available"`
	LocalDigest     string    `json:"local_digest,omitempty"`
	RemoteDigest    string    `json:"remote_digest,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// Watcher periodically asks the local Docker daemon whether the registry
// has a newer manifest for each image currently in use by running
// containers. Results are cached so the containers list endpoint can
// surface an "update available" flag without a network call per request.
//
// The watcher is intentionally best-effort: private registries that
// require auth, registries that don't honour the v2 manifest endpoint,
// and transient HTTP errors all degrade to "no info" rather than failing
// the whole list response. The single-container Preview path remains the
// authoritative check before an actual update is performed.
type Watcher struct {
	docker   *docker.Client
	interval time.Duration
	notifs   *notifications.Service

	mu    sync.RWMutex
	cache map[string]UpdateInfo

	stop chan struct{}
	wg   sync.WaitGroup
}

// SetNotifier wires in the bell-icon notification center so each
// newly-detected available image update emits one entry (per image,
// not per container — deduped by the cache).
func (w *Watcher) SetNotifier(n *notifications.Service) { w.notifs = n }

// NewWatcher returns an unstarted watcher with the given poll interval.
// Pass 0 to fall back to the default 30 minutes.
func NewWatcher(d *docker.Client, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &Watcher{
		docker:   d,
		interval: interval,
		cache:    make(map[string]UpdateInfo),
		stop:     make(chan struct{}),
	}
}

// Start kicks off the poll loop in a goroutine. Safe to call once; a
// second call before Stop is a no-op.
func (w *Watcher) Start(ctx context.Context) {
	if w == nil || w.docker == nil {
		return
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		// Initial poll on startup so the cache isn't empty for the
		// first 30 minutes the server is up.
		w.poll(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
				w.poll(ctx)
			}
		}
	}()
	slog.Info("image-update watcher started", "interval", w.interval)
}

// Stop blocks until the poll goroutine has exited.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stop:
		// already stopped
	default:
		close(w.stop)
	}
	w.wg.Wait()
}

// Get returns the cached info for an image reference (e.g. "nginx:1.27")
// or false when the watcher hasn't checked that image yet.
func (w *Watcher) Get(image string) (UpdateInfo, bool) {
	if w == nil {
		return UpdateInfo{}, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	info, ok := w.cache[normalizeImageRef(image)]
	return info, ok
}

// Snapshot returns a copy of the full cache. Callers (the
// /images/updates handler) are free to marshal it directly.
func (w *Watcher) Snapshot() map[string]UpdateInfo {
	if w == nil {
		return map[string]UpdateInfo{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make(map[string]UpdateInfo, len(w.cache))
	for k, v := range w.cache {
		out[k] = v
	}
	return out
}

// poll iterates running containers, deduplicates image refs, and asks
// Docker's distribution inspect endpoint for the remote manifest. The
// distribution endpoint speaks the registry v2 protocol — Docker Hub,
// GHCR, ECR (with auth pre-configured), Quay, and most others work
// out of the box. Anonymous auth covers public images; private images
// surface as Error="auth required" and stay UpdateAvailable=false.
func (w *Watcher) poll(ctx context.Context) {
	if w.docker == nil {
		return
	}
	cli := w.docker.Raw()
	containers, err := cli.ContainerList(ctx, dtypes.ContainerListOptions{All: false})
	if err != nil {
		slog.Debug("image-update watcher: list containers failed", "err", err)
		return
	}
	seen := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		ref := normalizeImageRef(c.Image)
		if ref == "" {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		info := UpdateInfo{Image: ref, CheckedAt: time.Now()}

		// Local digest for the image currently in use by this container.
		// We use the resolved image id rather than the ref so we always
		// see the same digest the container actually runs.
		if img, _, lerr := cli.ImageInspectWithRaw(ctx, c.ImageID); lerr == nil {
			if len(img.RepoDigests) > 0 {
				info.LocalDigest = trimDigest(img.RepoDigests[0])
			} else {
				info.LocalDigest = img.ID
			}
		}

		// Remote manifest. DistributionInspect speaks the registry v2
		// protocol and returns the descriptor (with digest) for the
		// remote ref. Empty registry-auth header → anonymous; private
		// registries return 401 which surfaces here as an error.
		dist, derr := cli.DistributionInspect(ctx, ref, "")
		if derr != nil {
			info.Error = trimErr(derr.Error())
		} else {
			info.RemoteDigest = trimDigest(string(dist.Descriptor.Digest))
			if info.LocalDigest != "" && info.RemoteDigest != "" &&
				info.LocalDigest != info.RemoteDigest {
				info.UpdateAvailable = true
			}
		}

		// Notification only when the update status flips from
		// not-available → available. Suppresses spam when an image
		// has been pending update for days and we poll every 30 min.
		w.mu.Lock()
		previous, hadPrevious := w.cache[ref]
		w.cache[ref] = info
		w.mu.Unlock()

		if w.notifs != nil && info.UpdateAvailable &&
			(!hadPrevious || !previous.UpdateAvailable) {
			_, _ = w.notifs.Emit(ctx, notifications.EmitInput{
				Kind:     notifications.KindImageUpdate,
				Severity: notifications.SevInfo,
				Title:    "Image update available: " + ref,
				Body:     "A newer manifest is published upstream",
				Link:     "/resources?tab=images",
			})
		}
	}
}

// trimDigest strips an optional "<ref>@" prefix from a repo-digest like
// "nginx@sha256:...". We only care about the sha part.
func trimDigest(s string) string {
	if i := strings.IndexByte(s, '@'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// trimErr keeps audit-log + json output reasonably small.
func trimErr(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
