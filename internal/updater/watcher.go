package updater

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
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

// checkRetention drops stored results for images no running container
// has used for this long.
const checkRetention = 30 * 24 * time.Hour

// Watcher periodically asks the local Docker daemon whether the registry
// has a newer manifest for each image currently in use by running
// containers. Results are cached (and stored, so a restart neither
// forgets them nor notifies again) so the containers list endpoint can
// surface an "update available" flag without a network call per request.
//
// The watcher is best-effort: registries that don't honour the v2
// manifest endpoint and transient HTTP errors degrade to "no info"
// rather than failing the whole list response.
type Watcher struct {
	docker   *docker.Client
	db       *sql.DB
	interval time.Duration
	notifs   *notifications.Service
	auth     authResolver

	mu       sync.RWMutex
	cache    map[string]UpdateInfo
	notified map[string]string

	stop chan struct{}
	wg   sync.WaitGroup
}

// SetNotifier wires in the bell-icon notification center so each
// newly-detected available image update emits one entry per image and
// remote digest.
func (w *Watcher) SetNotifier(n *notifications.Service) { w.notifs = n }

// SetAuthResolver lets the watcher check private images with the
// credentials stored under /registries.
func (w *Watcher) SetAuthResolver(r authResolver) { w.auth = r }

// NewWatcher returns an unstarted watcher with the given poll interval.
// Pass 0 to fall back to the default 30 minutes. db may be nil, then
// results only live in memory.
func NewWatcher(d *docker.Client, db *sql.DB, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	return &Watcher{
		docker:   d,
		db:       db,
		interval: interval,
		cache:    make(map[string]UpdateInfo),
		notified: make(map[string]string),
		stop:     make(chan struct{}),
	}
}

// Start kicks off the poll loop in a goroutine. Wire the notifier and
// auth resolver before calling it.
func (w *Watcher) Start(ctx context.Context) {
	if w == nil || w.docker == nil {
		return
	}
	w.load(ctx)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
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
	info, ok := w.cache[normalizeImageRef(baseRef(image))]
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

// MarkUpdated clears the "update available" flag for an image right
// after it was pulled, instead of showing it until the next poll.
func (w *Watcher) MarkUpdated(ctx context.Context, image string) {
	if w == nil {
		return
	}
	ref := normalizeImageRef(baseRef(image))
	w.mu.Lock()
	info, ok := w.cache[ref]
	if !ok || !info.UpdateAvailable {
		w.mu.Unlock()
		return
	}
	info.UpdateAvailable = false
	info.LocalDigest = info.RemoteDigest
	w.cache[ref] = info
	notified := w.notified[ref]
	w.mu.Unlock()
	w.store(ctx, info, notified)
}

// shouldNotify raises the bell once per remote digest: a pending update
// doesn't notify on every poll or restart, a newer release does.
func shouldNotify(info UpdateInfo, notifiedDigest string) bool {
	return info.UpdateAvailable && info.RemoteDigest != "" && info.RemoteDigest != notifiedDigest
}

// poll iterates running containers, deduplicates image refs, and asks
// Docker's distribution inspect endpoint for the remote manifest. The
// distribution endpoint speaks the registry v2 protocol — Docker Hub,
// GHCR, ECR, Quay, and most others work. Private images use the
// matching /registries credential when one exists.
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
		if c.Image == "" || strings.HasPrefix(c.Image, "sha256:") {
			continue
		}
		ref := normalizeImageRef(baseRef(c.Image))
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		info := UpdateInfo{Image: ref, CheckedAt: time.Now()}

		// The digest of the image the container actually runs, for
		// ref's repository. An image without one was built or loaded
		// locally and has nothing upstream to compare with.
		img, _, lerr := cli.ImageInspectWithRaw(ctx, c.ImageID)
		if lerr != nil {
			info.Error = trimErr(lerr.Error())
		} else {
			info.LocalDigest = repoDigestFor(ref, img.RepoDigests)
			if info.LocalDigest == "" {
				info.Error = "no registry digest (image built or loaded locally)"
			}
		}

		if info.LocalDigest != "" {
			var authBlob string
			if w.auth != nil {
				if a, aerr := w.auth.ResolveAuthForImage(ctx, ref, nil); aerr == nil {
					authBlob = a
				}
			}
			dist, derr := cli.DistributionInspect(ctx, ref, authBlob)
			if derr != nil {
				info.Error = trimErr(derr.Error())
			} else {
				info.RemoteDigest = trimDigest(string(dist.Descriptor.Digest))
				info.UpdateAvailable = info.RemoteDigest != "" && info.LocalDigest != info.RemoteDigest
			}
		}

		w.mu.Lock()
		notified := w.notified[ref]
		notify := shouldNotify(info, notified)
		if notify {
			notified = info.RemoteDigest
			w.notified[ref] = notified
		}
		w.cache[ref] = info
		w.mu.Unlock()
		w.store(ctx, info, notified)

		if notify && w.notifs != nil {
			_, _ = w.notifs.Emit(ctx, notifications.EmitInput{
				Kind:     notifications.KindImageUpdate,
				Severity: notifications.SevInfo,
				Title:    "Image update available: " + ref,
				Body:     "A newer manifest is published upstream",
				Link:     "/resources?tab=images",
				Perm:     rbac.PermImagesView,
			})
		}
	}
	w.expire(ctx)
}

// load restores the stored results so the first poll after a restart
// knows which updates were already announced.
func (w *Watcher) load(ctx context.Context) {
	if w.db == nil {
		return
	}
	rows, err := w.db.QueryContext(ctx, `
		SELECT image, update_available, local_digest, remote_digest, error, notified_digest, checked_at
		FROM image_update_checks`)
	if err != nil {
		slog.Warn("image-update watcher: load stored checks failed", "err", err)
		return
	}
	defer rows.Close()
	w.mu.Lock()
	defer w.mu.Unlock()
	for rows.Next() {
		var info UpdateInfo
		var notified string
		if err := rows.Scan(&info.Image, &info.UpdateAvailable, &info.LocalDigest, &info.RemoteDigest,
			&info.Error, &notified, &info.CheckedAt); err != nil {
			slog.Warn("image-update watcher: load stored check failed", "err", err)
			return
		}
		w.cache[info.Image] = info
		w.notified[info.Image] = notified
	}
	if err := rows.Err(); err != nil {
		slog.Warn("image-update watcher: load stored checks failed", "err", err)
	}
}

func (w *Watcher) store(ctx context.Context, info UpdateInfo, notified string) {
	if w.db == nil {
		return
	}
	_, err := w.db.ExecContext(ctx, `
		INSERT INTO image_update_checks
			(image, update_available, local_digest, remote_digest, error, notified_digest, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(image) DO UPDATE SET
			update_available = excluded.update_available,
			local_digest     = excluded.local_digest,
			remote_digest    = excluded.remote_digest,
			error            = excluded.error,
			notified_digest  = excluded.notified_digest,
			checked_at       = excluded.checked_at`,
		info.Image, info.UpdateAvailable, info.LocalDigest, info.RemoteDigest, info.Error, notified, info.CheckedAt.UTC())
	if err != nil {
		slog.Warn("image-update watcher: store check failed", "image", info.Image, "err", err)
	}
}

func (w *Watcher) expire(ctx context.Context) {
	cutoff := time.Now().Add(-checkRetention)
	w.mu.Lock()
	for ref, info := range w.cache {
		if info.CheckedAt.Before(cutoff) {
			delete(w.cache, ref)
			delete(w.notified, ref)
		}
	}
	w.mu.Unlock()
	if w.db == nil {
		return
	}
	if _, err := w.db.ExecContext(ctx, `DELETE FROM image_update_checks WHERE checked_at < ?`, cutoff.UTC()); err != nil {
		slog.Warn("image-update watcher: expire stored checks failed", "err", err)
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
