package migration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/dockmesh/dockmesh/internal/docker"
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
	project    string
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
			sv := stackVolume{name: v.Source, project: proj.Name}
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
			return nil, volumeDriverError(v.name)
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
			return nil, volumeDriverError(v.name)
		}
		// A volume another stack created under that name holds that
		// stack's data, which a finalised migration would remove here.
		if p := src.Labels["com.docker.compose.project"]; p != "" && p != v.project {
			return nil, fmt.Errorf("volume %s belongs to another stack and is not copied with this one", v.name)
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

func volumeDriverError(name string) error {
	return fmt.Errorf("volume %s uses a volume driver or driver options, whose data may not live on the host; move it yourself", name)
}

// labelMigration marks a volume a migration created on the target, so it
// only ever imports into and removes volumes it made itself.
const labelMigration = "dockmesh.migration"

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
		labels := map[string]string{labelMigration: m.ID}
		for k, val := range v.labels {
			if k != labelMigration {
				labels[k] = val
			}
		}
		// Docker hands back an existing volume of that name instead of
		// failing, so the marker tells whether this create made it.
		created, err := target.CreateVolume(ctx, v.name, "local", labels)
		if err != nil {
			return fmt.Errorf("create volume %s on the target: %w", v.name, err)
		}
		if created.Labels[labelMigration] != m.ID {
			return fmt.Errorf("volume %s appeared on the target during the migration; it is left as it is", v.name)
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

// removeCreatedVolumes removes the volumes m created on target, retrying
// for up to targetWaitMax in all while the target's Docker doesn't answer
// or a helper still holds a volume.
// A volume without m's marker is left alone. It returns the ones it could
// not remove.
func (s *Service) removeCreatedVolumes(ctx context.Context, m *Migration, target host.Host) []string {
	p := m.progress()
	deadline := time.Now().Add(targetWaitMax)
	var left []string
	for _, name := range p.CreatedVolumes {
		err := removeCreatedVolume(ctx, m, target, name, deadline)
		if errors.Is(err, errNotOurs) {
			slog.Warn("migration rollback: volume on the target was replaced since the migration", "id", m.ID, "volume", name)
			continue
		}
		if err != nil {
			slog.Warn("migration rollback: remove volume on the target", "id", m.ID, "volume", name, "err", err)
			left = append(left, name)
		}
	}
	p.CreatedVolumes = left
	return left
}

func removeCreatedVolume(ctx context.Context, m *Migration, target host.Host, name string, deadline time.Time) error {
	for {
		err := removeIfOurs(ctx, m, target, name)
		if err == nil || !mayClear(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(hostWait):
		}
	}
}

// mayClear reports whether err may clear by waiting: the host's Docker or
// agent didn't answer, or a Dockmesh helper still holds the volume.
func mayClear(err error) bool {
	if errors.Is(err, host.ErrAgentOffline) || errors.Is(err, host.ErrNoDocker) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connect to the docker daemon") || strings.Contains(msg, "docker daemon unavailable") ||
		strings.Contains(msg, "docker unavailable") {
		return true
	}
	return errors.Is(err, errHelperHolds)
}

func volumeInUse(err error) bool { return strings.Contains(err.Error(), "volume is in use") }

var errNotOurs = errors.New("the volume lacks this migration's marker; left as it is")

func removeIfOurs(ctx context.Context, m *Migration, target host.Host, name string) error {
	vol, err := target.InspectVolume(ctx, name)
	if host.VolumeMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if vol.Labels[labelMigration] != m.ID {
		return errNotOurs
	}
	err = target.RemoveVolume(ctx, name, false)
	if err != nil && volumeInUse(err) {
		// An agent killed during the copy leaves its helper behind.
		if !removeStoppedHelpers(ctx, target, name) {
			return err
		}
		if err = target.RemoveVolume(ctx, name, false); err != nil && volumeInUse(err) {
			return fmt.Errorf("%w: %w", errHelperHolds, err)
		}
	}
	if err != nil && !host.VolumeMissing(err) {
		return err
	}
	return nil
}

// errHelperHolds is a volume still mounted by one of Dockmesh's helpers,
// which exits on its own once the copy broke off.
var errHelperHolds = errors.New("a Dockmesh helper still uses the volume")

// removeStoppedHelpers removes Dockmesh's own stopped helper containers
// that mount volume name on h. It reports whether any helper, stopped or
// still running, mounts it.
func removeStoppedHelpers(ctx context.Context, h host.Host, name string) bool {
	list, err := h.ListContainers(ctx, true)
	if err != nil {
		return false
	}
	found := false
	for _, c := range list {
		if c.Labels["dockmesh.component"] != docker.BackupHelperComponent || !mountsVolume(c, name) {
			continue
		}
		found = true
		if c.State == container.StateRunning {
			continue
		}
		if err := h.RemoveContainer(ctx, c.ID, false); err != nil {
			slog.Warn("migration rollback: remove stopped helper", "container", c.ID, "err", err)
		}
	}
	return found
}

func mountsVolume(c container.Summary, name string) bool {
	for _, mt := range c.Mounts {
		if mt.Type == mount.TypeVolume && mt.Name == name {
			return true
		}
	}
	return false
}

func (s *Service) saveProgress(ctx context.Context, m *Migration) {
	if err := s.store.UpdateProgress(ctx, m.ID, m.progress()); err != nil {
		slog.Warn("migration progress", "id", m.ID, "err", err)
	}
}
