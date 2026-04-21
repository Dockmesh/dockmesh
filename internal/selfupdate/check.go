// Package selfupdate polls the Dockmesh GitHub Releases API once a day
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
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/settings"
)

const (
	// GitHub Releases API for the public Dockmesh repo. No auth required
	// — anonymous rate limit is 60 req/h which is more than enough for a
	// 24-hour poll.
	releasesURL = "https://api.github.com/repos/Dockmesh/dockmesh/releases/latest"

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

	// Max release notes bytes stored. Trimmed to keep the settings row
	// small — full notes are reachable via ReleaseURL.
	maxNotesLen = 2000
)

// Result is the public snapshot the UI renders. Empty LatestVersion means
// "never checked yet" (or check disabled). UpdateAvailable is computed
// against runtime version at read time so a binary built from a newer
// tag than the cached LatestVersion doesn't nag about itself.
type Result struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	UpdateAvailable bool      `json:"update_available"`
	IsDevBuild      bool      `json:"is_dev_build"`
	ReleaseURL      string    `json:"release_url"`
	ReleaseNotes    string    `json:"release_notes"`
	PublishedAt     time.Time `json:"published_at,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	Enabled         bool      `json:"enabled"`
	Error           string    `json:"error,omitempty"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// Checker owns the background goroutine + the in-memory cache. A single
// instance lives on main and is shared with the API handler.
type Checker struct {
	settings *settings.Store
	current  string
	client   *http.Client

	mu      sync.RWMutex
	lastErr string
}

// New wires a Checker to its dependencies. `current` is the runtime
// version string from pkg/version — pass "dev" for local builds and
// "v0.1.1"-style tags for releases.
func New(s *settings.Store, currentVersion string) *Checker {
	return &Checker{
		settings: s,
		current:  currentVersion,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
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

// CheckNow performs an immediate GitHub Releases lookup and persists
// the result to settings. Safe to call from an HTTP handler ("Check
// now" button). Never panics; errors are recorded on the Checker and
// surfaced via Status().Error.
func (c *Checker) CheckNow(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Dockmesh/"+c.current)

	resp, err := c.client.Do(req)
	if err != nil {
		c.recordErr(fmt.Errorf("fetch: %w", err))
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("github api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		c.recordErr(err)
		return err
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		c.recordErr(fmt.Errorf("decode: %w", err))
		return err
	}

	// Skip drafts and prereleases — the banner should only nag about
	// stable releases. A dedicated "include prereleases" toggle can come
	// later if users ask for it.
	if rel.Draft || rel.Prerelease || rel.TagName == "" {
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

	c.recordErr(nil)
	slog.Info("selfupdate: checked", "current", c.current, "latest", rel.TagName, "update_available", isNewer(c.current, rel.TagName) || isDevVersion(c.current))
	return nil
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

	return Result{
		CurrentVersion:  c.current,
		LatestVersion:   latest,
		UpdateAvailable: available,
		IsDevBuild:      dev,
		ReleaseURL:      c.settings.Get("update_release_url", ""),
		ReleaseNotes:    c.settings.Get("update_release_notes", ""),
		PublishedAt:     publishedAt,
		CheckedAt:       checkedAt,
		Enabled:         c.settings.GetBool("update_check_enabled", true),
		Error:           lastErr,
	}
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
// banner about itself.
func isNewer(current, latest string) bool {
	if latest == "" {
		return false
	}
	cur := strings.TrimPrefix(current, "v")
	lat := strings.TrimPrefix(latest, "v")
	if cur == "" || cur == "dev" || cur == "unknown" {
		return false
	}
	curParts := parseSemver(cur)
	latParts := parseSemver(lat)
	for i := 0; i < 3; i++ {
		if latParts[i] > curParts[i] {
			return true
		}
		if latParts[i] < curParts[i] {
			return false
		}
	}
	return false
}

// parseSemver extracts [major, minor, patch] from a "1.2.3" or "1.2.3-rc1"
// style string. Unparseable segments become 0 so a malformed tag doesn't
// falsely trigger an update banner.
func parseSemver(v string) [3]int {
	// Drop any pre-release / build metadata suffix.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	out := [3]int{}
	for i := 0; i < len(parts) && i < 3; i++ {
		n := 0
		for _, r := range parts[i] {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out
}
