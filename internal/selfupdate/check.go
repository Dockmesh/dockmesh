// Package selfupdate polls the Dockmesh GitHub Releases API every 2 hours by default
// to find out whether a newer version has been published. Result is
// cached in the settings table so restarts don't re-fetch; the UI reads
// Status() to render the "Update available" banner.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/changelog"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/semver"
	"github.com/dockmesh/dockmesh/internal/settings"
)

const (
	// GitHub Releases API for the public Dockmesh repo. No auth required
	// — anonymous rate limit is 60 req/h which is more than enough for a
	// 24-hour poll.
	apiBase = "https://api.github.com/repos/Dockmesh/dockmesh"

	// releasesPage is how many of the newest releases the pre-release
	// channel compares; GitHub lists them newest first.
	releasesPage = 30

	// Default poll interval — 2h is a reasonable compromise for a
	// project in active release mode (multiple versions/day possible)
	// while staying well under GitHub's 60 req/h anonymous rate limit.
	// Admin can override via the `update_check_interval_minutes` setting.
	defaultCheckInterval = 2 * time.Hour
	minCheckInterval     = 15 * time.Minute // don't hammer GitHub
	maxCheckInterval     = 7 * 24 * time.Hour

	// How long to wait after boot before the first check. Gives the
	// server time to finish startup without blocking on a slow GitHub
	// response.
	bootDelay = 30 * time.Second

	// manifestAsset is the machine-readable release section published
	// with every release; assetPrefix is where release assets live.
	manifestAsset = "release-manifest.json"
	assetPrefix   = "https://github.com/Dockmesh/dockmesh/releases/download/"

	// Max manifest bytes read; real manifests are a few KB.
	maxManifestLen = 256 << 10

	// Max release notes bytes stored. Trimmed to keep the settings row
	// small — full notes are reachable via ReleaseURL.
	maxNotesLen = 2000
)

// ChannelKey is the setting that selects the releases the check offers.
// Unset means stable, so installs that predate the setting keep their
// behaviour.
const ChannelKey = "update_channel"

const (
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// CacheKeys are the settings the update check keeps for itself. Only the
// check writes them; the settings API neither lists nor accepts them.
var CacheKeys = []string{
	"update_last_check",
	"update_latest_version",
	"update_release_url",
	"update_release_notes",
	"update_published_at",
	keyNotifiedVersion,
	keyManifest,
}

// IsCacheKey reports whether key is one of CacheKeys.
func IsCacheKey(key string) bool { return slices.Contains(CacheKeys, key) }

// ReleaseURL returns u, re-encoded, if it is a page of a Dockmesh release on GitHub
// and "" otherwise, so the UI never links anywhere else.
func ReleaseURL(u string) string {
	if strings.Contains(u, `\`) || strings.Contains(strings.ToLower(u), "%5c") {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host != "github.com" || p.User != nil ||
		!strings.HasPrefix(p.Path, "/Dockmesh/dockmesh/releases/") || path.Clean(p.Path) != strings.TrimSuffix(p.Path, "/") {
		return ""
	}
	return p.String()
}

// ValidChannel reports whether v may be stored under ChannelKey; empty
// stands for stable.
func ValidChannel(v string) bool {
	return v == "" || v == ChannelStable || v == ChannelPrerelease
}

// Result is the public snapshot the UI renders. Empty LatestVersion means
// "never checked yet" (or check disabled). UpdateAvailable is computed
// against runtime version at read time so a binary built from a newer
// tag than the cached LatestVersion doesn't nag about itself.
type Result struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	// LatestPrerelease is set when LatestVersion is a release candidate,
	// which get.dockmesh.dev doesn't serve.
	LatestPrerelease bool      `json:"latest_prerelease"`
	Channel          string    `json:"channel"`
	UpdateAvailable  bool      `json:"update_available"`
	IsDevBuild       bool      `json:"is_dev_build"`
	ReleaseURL       string    `json:"release_url"`
	ReleaseNotes     string    `json:"release_notes"`
	PublishedAt      time.Time `json:"published_at,omitempty"`
	CheckedAt        time.Time `json:"checked_at,omitempty"`
	Enabled          bool      `json:"enabled"`
	Error            string    `json:"error,omitempty"`
	// Severity and Security come from the latest release's manifest and
	// are empty without an update or a manifest.
	Severity string   `json:"severity,omitempty"`
	Security []string `json:"security,omitempty"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Checker owns the background goroutine + the in-memory cache. A single
// instance lives on main and is shared with the API handler.
type Checker struct {
	settings *settings.Store
	current  string
	client   *http.Client
	notifs   *notifications.Service
	// assetPrefix is the only URL prefix a manifest is fetched from.
	assetPrefix string
	apiBase     string

	mu      sync.RWMutex
	lastErr string
}

// SetNotifier wires in the bell-icon notification center so a new
// upstream release fires one notification (not one per poll cycle —
// the checker dedups by version).
func (c *Checker) SetNotifier(n *notifications.Service) { c.notifs = n }

// New wires a Checker to its dependencies. `current` is the runtime
// version string from pkg/version — pass "dev" for local builds and
// "v0.1.1"-style tags for releases.
func New(s *settings.Store, currentVersion string) *Checker {
	c := &Checker{
		settings: s,
		current:  currentVersion,
		client:   &http.Client{Timeout: 10 * time.Second},

		assetPrefix: assetPrefix,
		apiBase:     apiBase,
	}
	if labAPIBase != nil {
		if b := labAPIBase(); b != "" {
			c.apiBase, c.assetPrefix = b, ReleaseBaseURL()
		}
	}
	return c
}

// Start spins off the polling goroutine. Returns immediately.
func (c *Checker) Start(ctx context.Context) {
	go c.run(ctx)
}

func (c *Checker) run(ctx context.Context) {
	// Wait bootDelay before the first check so we don't hammer GitHub
	// during startup bursts (e.g. systemd restart loops).
	select {
	case <-time.After(bootDelay):
	case <-ctx.Done():
		return
	}

	c.tick(ctx)
	// Re-read the interval every loop so a settings change takes effect
	// on the next cycle without a restart. No ticker because the
	// interval is mutable.
	for {
		interval := c.pollInterval()
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			c.tick(ctx)
		}
	}
}

// pollInterval reads the current polling cadence from settings, clamped
// to [minCheckInterval, maxCheckInterval] so a bad value in the DB can't
// either DoS GitHub or silently disable checks.
func (c *Checker) pollInterval() time.Duration {
	raw := c.settings.Get("update_check_interval_minutes", "")
	if raw == "" {
		return defaultCheckInterval
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return defaultCheckInterval
		}
		n = n*10 + int(r-'0')
	}
	d := time.Duration(n) * time.Minute
	if d < minCheckInterval {
		return minCheckInterval
	}
	if d > maxCheckInterval {
		return maxCheckInterval
	}
	return d
}

func (c *Checker) tick(ctx context.Context) {
	if !c.settings.GetBool("update_check_enabled", true) {
		return
	}
	if err := c.CheckNow(ctx); err != nil {
		slog.Warn("selfupdate: check failed", "err", err)
	}
}

// Channel returns the configured update channel.
func (c *Checker) Channel() string {
	if c.settings.Get(ChannelKey, "") == ChannelPrerelease {
		return ChannelPrerelease
	}
	return ChannelStable
}

// CheckNow performs an immediate GitHub Releases lookup and persists
// the result to settings. Safe to call from an HTTP handler ("Check
// now" button). Never panics; errors are recorded on the Checker and
// surfaced via Status().Error.
func (c *Checker) CheckNow(ctx context.Context) error {
	var rel *ghRelease
	if c.Channel() == ChannelPrerelease {
		var rels []ghRelease
		if err := c.getJSON(ctx, fmt.Sprintf("%s/releases?per_page=%d", c.apiBase, releasesPage), &rels); err != nil {
			c.recordErr(err)
			return err
		}
		rel = newestRelease(rels)
	} else {
		var latest ghRelease
		if err := c.getJSON(ctx, c.apiBase+"/releases/latest", &latest); err != nil {
			c.recordErr(err)
			return err
		}
		if !latest.Draft && !latest.Prerelease && latest.TagName != "" && !isPrerelease(latest.TagName) {
			rel = &latest
		}
	}
	if rel == nil {
		c.recordErr(nil)
		return nil
	}

	notes := rel.Body
	if len(notes) > maxNotesLen {
		notes = notes[:maxNotesLen] + "…"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	published := ""
	if !rel.PublishedAt.IsZero() {
		published = rel.PublishedAt.UTC().Format(time.RFC3339)
	}
	_ = c.settings.Set(ctx, "update_last_check", now)
	_ = c.settings.Set(ctx, "update_latest_version", rel.TagName)
	_ = c.settings.Set(ctx, "update_release_url", rel.HTMLURL)
	_ = c.settings.Set(ctx, "update_release_notes", notes)
	_ = c.settings.Set(ctx, "update_published_at", published)
	_ = c.settings.Set(ctx, keyManifest, c.fetchManifest(ctx, *rel))

	c.recordErr(nil)
	updateAvailable := isNewer(c.current, rel.TagName) || isDevVersion(c.current)
	slog.Info("selfupdate: checked", "current", c.current, "latest", rel.TagName, "update_available", updateAvailable)

	if updateAvailable {
		c.notifyAvailable(ctx, rel.TagName)
	}
	return nil
}

func (c *Checker) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Dockmesh/"+c.current)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// newestRelease returns the highest published version among rels, stable
// or pre-release, or nil when there is none.
func newestRelease(rels []ghRelease) *ghRelease {
	var best *ghRelease
	var bestV semver.Version
	for i := range rels {
		r := &rels[i]
		if r.Draft {
			continue
		}
		v, ok := semver.Parse(r.TagName)
		if !ok {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best
}

// keyNotifiedVersion is the release admins were last told about. Stored,
// so a restart doesn't announce the same version again.
const keyNotifiedVersion = "update_last_notified_version"

func (c *Checker) notifyAvailable(ctx context.Context, tag string) {
	if c.notifs == nil || c.settings.Stored(keyNotifiedVersion) == tag {
		return
	}
	if _, err := c.notifs.Emit(ctx, notifications.EmitInput{
		Kind:     notifications.KindSystemUpgrade,
		Severity: notifications.SevInfo,
		Title:    "Dockmesh " + tag + " available",
		Body:     "Current version: " + c.current,
		Link:     "/settings/updates",
		Perm:     rbac.PermSystemUpgrade,
	}); err != nil {
		slog.Warn("selfupdate: notification failed", "version", tag, "err", err)
		return
	}
	if err := c.settings.Set(ctx, keyNotifiedVersion, tag); err != nil {
		slog.Warn("selfupdate: notified version not saved", "version", tag, "err", err)
	}
}

// keyManifest caches the latest release's manifest as JSON; empty when the
// release has none.
const keyManifest = "update_release_manifest"

// fetchManifest downloads the release's manifest and returns it as JSON,
// or "" when the release has none or it can't be read. The banner works
// without it, so failures are only logged.
func (c *Checker) fetchManifest(ctx context.Context, rel ghRelease) string {
	url := ""
	for _, a := range rel.Assets {
		if a.Name == manifestAsset {
			url = a.URL
		}
	}
	if url == "" || !strings.HasPrefix(url, c.assetPrefix) {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Dockmesh/"+c.current)
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Warn("selfupdate: fetch release manifest", "err", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("selfupdate: fetch release manifest", "status", resp.StatusCode)
		return ""
	}
	var m changelog.Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxManifestLen)).Decode(&m); err != nil {
		slog.Warn("selfupdate: decode release manifest", "err", err)
		return ""
	}
	if m.Version != strings.TrimPrefix(rel.TagName, "v") {
		slog.Warn("selfupdate: release manifest is for another version", "manifest", m.Version, "tag", rel.TagName)
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// LatestManifest returns the manifest of the latest release when that
// release is newer than the running version, or nil.
func (c *Checker) LatestManifest() *changelog.Manifest {
	st := c.Status()
	if !st.UpdateAvailable {
		return nil
	}
	return c.manifestFor(st.LatestVersion)
}

// manifestFor returns the cached manifest when it belongs to tag.
func (c *Checker) manifestFor(tag string) *changelog.Manifest {
	raw := c.settings.Get(keyManifest, "")
	if raw == "" || tag == "" {
		return nil
	}
	var m changelog.Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	if m.Version != strings.TrimPrefix(tag, "v") {
		return nil
	}
	return &m
}

// securityEntries lists the manifest's advisories and security entries.
func securityEntries(m *changelog.Manifest) []string {
	var out []string
	for _, a := range m.Advisories {
		out = append(out, strings.TrimSpace(a.ID+" ("+a.Severity+"): "+a.Title))
	}
	return append(out, m.Changes.Security...)
}

func (c *Checker) recordErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.lastErr = ""
	} else {
		c.lastErr = err.Error()
	}
}

// Status returns the cached result + freshly-computed UpdateAvailable
// flag. No blocking, no network.
func (c *Checker) Status() Result {
	latest := c.settings.Get("update_latest_version", "")
	channel := c.Channel()
	// The cache may still hold a candidate found before the channel was
	// switched back to stable.
	if channel == ChannelStable && isPrerelease(latest) {
		latest = ""
	}
	checkedAtStr := c.settings.Get("update_last_check", "")
	publishedStr := c.settings.Get("update_published_at", "")

	var checkedAt, publishedAt time.Time
	if t, err := time.Parse(time.RFC3339, checkedAtStr); err == nil {
		checkedAt = t
	}
	if t, err := time.Parse(time.RFC3339, publishedStr); err == nil {
		publishedAt = t
	}

	c.mu.RLock()
	lastErr := c.lastErr
	c.mu.RUnlock()

	// Dev builds: any published release is treated as "newer" so the
	// self-hosted-from-source operator still gets the upgrade banner.
	// A proper tagged build only shows available when semver says so.
	dev := isDevVersion(c.current)
	available := false
	if latest != "" {
		if dev {
			available = true
		} else {
			available = isNewer(c.current, latest)
		}
	}

	res := Result{
		CurrentVersion:   c.current,
		LatestVersion:    latest,
		LatestPrerelease: isPrerelease(latest),
		Channel:          channel,
		UpdateAvailable:  available,
		IsDevBuild:       dev,
		ReleaseURL:       ReleaseURL(c.settings.Get("update_release_url", "")),
		ReleaseNotes:     c.settings.Get("update_release_notes", ""),
		PublishedAt:      publishedAt,
		CheckedAt:        checkedAt,
		Enabled:          c.settings.GetBool("update_check_enabled", true),
		Error:            lastErr,
	}
	if available {
		if m := c.manifestFor(latest); m != nil {
			res.Severity = m.Severity
			res.Security = securityEntries(m)
		}
	}
	return res
}

// isDevVersion returns true for locally-built binaries where no release
// tag was injected via ldflags. These should still see the banner so
// the operator knows a published release exists.
func isDevVersion(v string) bool {
	if v == "" || v == "dev" || v == "unknown" {
		return true
	}
	// Anything not starting with a digit or "v" is also a dev build
	// (branch name, commit sha, "HEAD", etc.).
	s := strings.TrimPrefix(v, "v")
	if s == "" || s[0] < '0' || s[0] > '9' {
		return true
	}
	return false
}

// isNewer returns true when `latest` is a newer semver tag than `current`.
// Both inputs may include a leading "v". Dev builds (current=="dev") are
// treated as "up-to-date" — local development shouldn't show an update
// banner about itself. A release candidate is older than its release, so
// an install running v0.6.0-rc.2 is offered v0.6.0.
func isNewer(current, latest string) bool { return semver.Newer(latest, current) }

func isPrerelease(tag string) bool {
	v, ok := semver.Parse(tag)
	return ok && v.Prerelease()
}
