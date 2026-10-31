package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
)

// CaddyVersion is a Caddy release tested with this Dockmesh version,
// pinned by digest.
type CaddyVersion struct {
	Version string `json:"version"`
	Image   string `json:"image"`
}

// TestedVersions is oldest first; the last entry is the default.
var TestedVersions = []CaddyVersion{
	{"2.11.6", "caddy:2.11.6@sha256:3422ce6de165df66534f9b9ba50efaf457114ec961763cc52f5dbdaac2972d73"},
	{"2.11.7", DefaultProxyImage},
}

var (
	ErrVersionReadOnly     = errors.New("the Caddy image is set with DOCKMESH_PROXY_IMAGE, so Dockmesh doesn't manage its version")
	ErrUnknownVersion      = errors.New("not a Caddy version tested with this Dockmesh release")
	ErrNoPreviousVersion   = errors.New("no previous Caddy version to roll back to")
	ErrVersionSwitchFailed = errors.New("caddy version switch failed")
)

// VersionInfo describes which Caddy runs and which it can switch to.
type VersionInfo struct {
	// Current is empty when a custom image runs.
	Current   string         `json:"current"`
	Image     string         `json:"image"`
	Default   string         `json:"default"`
	Previous  string         `json:"previous,omitempty"`
	Available []CaddyVersion `json:"available"`
	// Managed is false while DOCKMESH_PROXY_IMAGE is set;
	// ReadOnlyReason says why.
	Managed        bool   `json:"managed"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
}

func findVersion(v string) (CaddyVersion, bool) {
	for _, tv := range TestedVersions {
		if tv.Version == v {
			return tv, true
		}
	}
	return CaddyVersion{}, false
}

func versionOfImage(img string) string {
	for _, tv := range TestedVersions {
		if tv.Image == img {
			return tv.Version
		}
	}
	return ""
}

func (s *Service) currentImage() string {
	s.imageMu.RLock()
	defer s.imageMu.RUnlock()
	return s.image
}

func (s *Service) setImage(img string) {
	s.imageMu.Lock()
	s.image = img
	s.imageMu.Unlock()
}

// applyStoredVersion selects the version saved by the last switch. A
// version this release no longer ships falls back to the default.
func (s *Service) applyStoredVersion() {
	if s.imageOverride || s.settings == nil {
		return
	}
	v := s.settings.Get(KeyCaddyVersion, "")
	if v == "" {
		return
	}
	tv, ok := findVersion(v)
	if !ok {
		slog.Info("proxy: stored Caddy version is not shipped by this release, using the default", "stored", v, "default", versionOfImage(DefaultProxyImage))
		return
	}
	s.setImage(tv.Image)
}

// VersionInfo reports the running and available Caddy versions.
func (s *Service) VersionInfo() *VersionInfo {
	img := s.currentImage()
	info := &VersionInfo{
		Current:   versionOfImage(img),
		Image:     img,
		Default:   versionOfImage(DefaultProxyImage),
		Available: TestedVersions,
		Managed:   !s.imageOverride,
	}
	if s.imageOverride {
		info.ReadOnlyReason = ErrVersionReadOnly.Error()
	}
	if s.settings != nil {
		info.Previous = s.settings.Get(KeyCaddyPrevious, "")
	}
	return info
}

// SwitchVersion moves the proxy to another tested Caddy version. When
// the proxy runs, the container is recreated with the new image and
// must load the current config; otherwise the old version is restored
// and ErrVersionSwitchFailed returned. The image of the version left
// behind is kept for a rollback; older tested images are removed.
func (s *Service) SwitchVersion(ctx context.Context, version string) (*VersionInfo, error) {
	if s.imageOverride {
		return nil, ErrVersionReadOnly
	}
	target, ok := findVersion(version)
	if !ok {
		return nil, ErrUnknownVersion
	}
	if s.settings == nil {
		return nil, errors.New("settings store unavailable")
	}
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	// Held across the enabled check and the restart, so a disable in
	// between isn't undone.
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	oldImage := s.currentImage()
	if oldImage == target.Image {
		return s.VersionInfo(), nil
	}
	s.setImage(target.Image)
	if s.Enabled() {
		if err := s.startAndVerify(ctx); err != nil {
			s.setImage(oldImage)
			if rbErr := s.startAndVerify(ctx); rbErr != nil {
				return nil, fmt.Errorf("%w: Caddy %s did not come up with the config (%w); going back to the previous image failed too: %w",
					ErrVersionSwitchFailed, target.Version, err, rbErr)
			}
			return nil, fmt.Errorf("%w: Caddy %s did not come up with the config (%w); rolled back", ErrVersionSwitchFailed, target.Version, err)
		}
	}
	if err := s.settings.Set(ctx, KeyCaddyVersion, target.Version); err != nil {
		return nil, err
	}
	if prev := versionOfImage(oldImage); prev != "" {
		if err := s.settings.Set(ctx, KeyCaddyPrevious, prev); err != nil {
			return nil, err
		}
	}
	s.pruneImages(ctx, target.Image, oldImage)
	return s.VersionInfo(), nil
}

// RollbackVersion switches back to the version before the last switch.
func (s *Service) RollbackVersion(ctx context.Context) (*VersionInfo, error) {
	if s.imageOverride {
		return nil, ErrVersionReadOnly
	}
	if s.settings == nil {
		return nil, ErrNoPreviousVersion
	}
	prev := s.settings.Get(KeyCaddyPrevious, "")
	if _, ok := findVersion(prev); !ok {
		return nil, ErrNoPreviousVersion
	}
	return s.SwitchVersion(ctx, prev)
}

// startAndVerify recreates the container with the current image and
// checks that Caddy runs the config Dockmesh generates. Callers hold
// s.lifecycle.
func (s *Service) startAndVerify(ctx context.Context) error {
	if s.startProxy != nil {
		return s.startProxy(ctx)
	}
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	if err := s.enableLocked(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running, err := runningConfig(ctx)
	if err != nil {
		return err
	}
	want, err := s.desiredConfig(ctx)
	if err != nil {
		return err
	}
	if !sameConfig(running, want) {
		return errors.New("the running config differs from the routes")
	}
	return nil
}

// pruneImages removes tested Caddy images other than keep. Images still
// used by a container are left alone by Docker.
func (s *Service) pruneImages(ctx context.Context, keep ...string) {
	if s.docker == nil {
		return
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, tv := range TestedVersions {
		if kept[tv.Image] {
			continue
		}
		ref := digestRef(tv.Image)
		if _, err := s.docker.Raw().ImageRemove(ctx, ref, image.RemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			slog.Debug("proxy: removing old Caddy image failed", "image", ref, "err", err)
		}
	}
}

// digestRef turns name:tag@sha256:… into name@sha256:…, the form Docker
// accepts for removal.
func digestRef(img string) string {
	name, digest, ok := strings.Cut(img, "@")
	if !ok {
		return img
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	return name + "@" + digest
}
