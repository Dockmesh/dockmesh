package migration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
)

// VolumeIO archives and imports the volumes of one host.
type VolumeIO interface {
	VolumeTar(ctx context.Context, name string) (io.ReadCloser, error)
	VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, needBytes int64) error
}

// SetVolumeIO wires in how a host's volumes are read and written; without
// it, stacks with named volumes can't migrate.
func (s *Service) SetVolumeIO(f func(hostID string) (VolumeIO, error)) { s.volumeIO = f }

// stackVolume is a named volume a stack's services mount.
type stackVolume struct {
	name       string
	external   bool
	driver     string
	driverOpts map[string]string
}

// stackVolumes returns the stack's named volumes by Docker name.
func (s *Service) stackVolumes(ctx context.Context, stackName string) ([]stackVolume, error) {
	proj, err := s.loadProject(ctx, stackName)
	if err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	byName := map[string]stackVolume{}
	for _, svc := range proj.Services {
		for _, v := range svc.Volumes {
			if v.Type != "volume" || v.Source == "" {
				continue
			}
			sv := stackVolume{name: v.Source}
			if vol, ok := proj.Volumes[v.Source]; ok {
				sv.name = vol.Name
				if sv.name == "" {
					sv.name = proj.Name + "_" + v.Source
				}
				sv.external = bool(vol.External)
				sv.driver = vol.Driver
				sv.driverOpts = vol.DriverOpts
			}
			byName[sv.name] = sv
		}
	}
	out := make([]stackVolume, 0, len(byName))
	for _, v := range byName {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// volumeToCopy is a volume a migration copies, with the labels the copy
// gets.
type volumeToCopy struct {
	name   string
	labels map[string]string
}

// volumePlan checks the stack's named volumes against both hosts and
// returns the ones to copy. External volumes are not copied; they must
// exist on the target.
func (s *Service) volumePlan(ctx context.Context, m *Migration, source, target host.Host) ([]volumeToCopy, error) {
	vols, err := s.stackVolumes(ctx, m.StackName)
	if err != nil {
		return nil, err
	}
	if len(vols) == 0 {
		return nil, nil
	}
	var out []volumeToCopy
	for _, v := range vols {
		if v.external {
			if _, err := target.InspectVolume(ctx, v.name); err != nil {
				if host.VolumeMissing(err) {
					return nil, fmt.Errorf("external volume %s is not copied and doesn't exist on the target; create it there first", v.name)
				}
				return nil, fmt.Errorf("volume %s on the target: %w", v.name, err)
			}
			continue
		}
		if (v.driver != "" && v.driver != "local") || len(v.driverOpts) > 0 {
			return nil, fmt.Errorf("volume %s uses a volume driver or driver options, whose data may not live on the host; move it yourself", v.name)
		}
		src, err := source.InspectVolume(ctx, v.name)
		if host.VolumeMissing(err) {
			// Never created on the source: the deploy on the target
			// creates it empty, as it was.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("volume %s on the source: %w", v.name, err)
		}
		if src.Labels["com.docker.stack.namespace"] != "" {
			return nil, fmt.Errorf("volume %s belongs to a Swarm stack and is not copied", v.name)
		}
		if (src.Driver != "" && src.Driver != "local") || len(src.Options) > 0 {
			return nil, fmt.Errorf("volume %s uses a volume driver or driver options, whose data may not live on the host; move it yourself", v.name)
		}
		if _, err := target.InspectVolume(ctx, v.name); err == nil {
			return nil, fmt.Errorf("volume %s already exists on the target; remove it there first, it would not be overwritten", v.name)
		} else if !host.VolumeMissing(err) {
			return nil, fmt.Errorf("volume %s on the target: %w", v.name, err)
		}
		out = append(out, volumeToCopy{name: v.name, labels: src.Labels})
	}
	if len(out) == 0 {
		return nil, nil
	}
	if s.volumeIO == nil {
		return nil, errors.New("volume transfer is not available on this server")
	}
	if err := host.VolumeTransferError(source, false); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if err := host.VolumeTransferError(target, true); err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	return out, nil
}

// progressEvery is how often a running copy stores its progress.
var progressEvery = 2 * time.Second

// copyVolumes creates each volume on the target, copies the source's data
// into it and reads it back to compare. Each created volume is recorded
// before data goes in, so a rollback can remove it again.
func (s *Service) copyVolumes(ctx context.Context, m *Migration, target host.Host, vols []volumeToCopy) error {
	srcIO, err := s.volumeIO(m.SourceHostID)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	tgtIO, err := s.volumeIO(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	p := m.progress()
	p.VolumesTotal = len(vols)
	for i, v := range vols {
		p.VolumeIndex = i + 1
		p.CurrentVolume = v.name
		s.saveProgress(ctx, m)
		if _, err := target.CreateVolume(ctx, v.name, "local", v.labels); err != nil {
			return fmt.Errorf("create volume %s on the target: %w", v.name, err)
		}
		p.CreatedVolumes = append(p.CreatedVolumes, v.name)
		s.saveProgress(ctx, m)

		// onBytes runs in copyVolume's reader while this goroutine waits.
		base := p.BytesDone
		last := time.Now()
		onBytes := func(n int64) {
			p.BytesDone = base + n
			if time.Since(last) >= progressEvery {
				last = time.Now()
				s.saveProgress(ctx, m)
			}
		}
		sent, err := copyVolume(ctx, srcIO, tgtIO, v.name, onBytes)
		if err != nil {
			return fmt.Errorf("copy volume %s: %w", v.name, err)
		}
		p.BytesDone = base + sent.bytes
		copied, err := readBack(ctx, tgtIO, v.name)
		if err != nil {
			return fmt.Errorf("read back volume %s on the target: %w", v.name, err)
		}
		if err := sent.diff(copied); err != nil {
			return fmt.Errorf("volume %s on the target differs from the source: %w", v.name, err)
		}
		p.VolumesCopied = append(p.VolumesCopied, CopiedVolume{Name: v.name, Bytes: sent.bytes, Entries: len(sent.entries)})
		slog.Info("migration volume copied", "id", m.ID, "volume", v.name, "bytes", sent.bytes, "entries", len(sent.entries))
	}
	p.CurrentVolume = ""
	s.saveProgress(ctx, m)
	return nil
}

// copyVolume streams volume name from src into the same volume on dst
// and returns the manifest of what was sent.
func copyVolume(ctx context.Context, src, dst VolumeIO, name string, onBytes func(int64)) (*manifest, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc, err := src.VolumeTar(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read on the source: %w", err)
	}
	defer rc.Close()
	pr, pw := io.Pipe()
	type result struct {
		m   *manifest
		err error
	}
	done := make(chan result, 1)
	go func() {
		m, err := readManifest(pr, onBytes)
		// Keeps the import's reads from blocking on a reader that gave up.
		_, _ = io.Copy(io.Discard, pr)
		done <- result{m, err}
	}()
	// The volume is new and empty; replace mode extracts into a staging
	// directory and takes the archive root's owner and mode for the
	// volume itself.
	ierr := dst.VolumeImport(ctx, name, io.TeeReader(rc, pw), true, 0)
	_ = pw.CloseWithError(ierr)
	res := <-done
	if ierr != nil {
		return nil, fmt.Errorf("write on the target: %w", ierr)
	}
	if res.err != nil {
		return nil, res.err
	}
	return res.m, nil
}

func readBack(ctx context.Context, src VolumeIO, name string) (*manifest, error) {
	rc, err := src.VolumeTar(ctx, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return readManifest(rc, nil)
}

// removeCreatedVolumes removes the volumes m created on target. It
// returns the ones it could not remove.
func (s *Service) removeCreatedVolumes(ctx context.Context, m *Migration, target host.Host) []string {
	p := m.progress()
	var left []string
	for _, name := range p.CreatedVolumes {
		if err := target.RemoveVolume(ctx, name, false); err != nil && !host.VolumeMissing(err) {
			slog.Warn("migration rollback: remove volume on the target", "id", m.ID, "volume", name, "err", err)
			left = append(left, name)
		}
	}
	p.CreatedVolumes = left
	return left
}

func (s *Service) saveProgress(ctx context.Context, m *Migration) {
	if err := s.store.UpdateProgress(ctx, m.ID, m.progress()); err != nil {
		slog.Warn("migration progress", "id", m.ID, "err", err)
	}
}
