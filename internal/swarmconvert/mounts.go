package swarmconvert

import (
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/mount"
)

func convertMounts(stack string, svc types.ServiceConfig, volumes types.Volumes) ([]mount.Mount, error) {
	if len(svc.Volumes) == 0 {
		return nil, nil
	}
	out := make([]mount.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		m, err := convertMount(stack, v, volumes)
		if err != nil {
			return nil, fmt.Errorf("volumes %s: %w", v.Target, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func convertMount(stack string, v types.ServiceVolumeConfig, volumes types.Volumes) (mount.Mount, error) {
	m := mount.Mount{
		Type:        mount.Type(v.Type),
		Source:      v.Source,
		Target:      v.Target,
		ReadOnly:    v.ReadOnly,
		Consistency: mount.Consistency(v.Consistency),
	}
	only := func(allowed string) error {
		for name, present := range map[string]bool{
			"bind": v.Bind != nil, "volume": v.Volume != nil, "tmpfs": v.Tmpfs != nil, "image": v.Image != nil,
		} {
			if present && name != allowed {
				return fmt.Errorf("%s options are incompatible with type %s", name, m.Type)
			}
		}
		return nil
	}
	switch v.Type {
	case types.VolumeTypeVolume, "":
		m.Type = mount.TypeVolume
		if err := only("volume"); err != nil {
			return mount.Mount{}, err
		}
		if v.Source == "" {
			return m, nil
		}
		vol, ok := volumes[v.Source]
		if !ok {
			return mount.Mount{}, fmt.Errorf("undefined volume %q", v.Source)
		}
		m.Source = scoped(stack, v.Source)
		if vol.Name != "" {
			m.Source = vol.Name
		}
		m.VolumeOptions = &mount.VolumeOptions{}
		if v.Volume != nil {
			m.VolumeOptions.NoCopy = v.Volume.NoCopy
			m.VolumeOptions.Subpath = v.Volume.Subpath
		}
		if vol.External {
			return m, nil
		}
		labels := map[string]string{}
		for k, val := range vol.Labels {
			labels[k] = val
		}
		labels[LabelNamespace] = stack
		m.VolumeOptions.Labels = labels
		if vol.Driver != "" || vol.DriverOpts != nil {
			m.VolumeOptions.DriverConfig = &mount.Driver{Name: vol.Driver, Options: vol.DriverOpts}
		}
	case types.VolumeTypeBind:
		if err := only("bind"); err != nil {
			return mount.Mount{}, err
		}
		if v.Source == "" {
			return mount.Mount{}, fmt.Errorf("bind mount without a source")
		}
		if v.Bind != nil && v.Bind.Propagation != "" {
			m.BindOptions = &mount.BindOptions{Propagation: mount.Propagation(v.Bind.Propagation)}
		}
	case types.VolumeTypeTmpfs:
		if err := only("tmpfs"); err != nil {
			return mount.Mount{}, err
		}
		if v.Source != "" {
			return mount.Mount{}, fmt.Errorf("tmpfs mount with a source")
		}
		if v.Tmpfs != nil {
			m.TmpfsOptions = &mount.TmpfsOptions{SizeBytes: int64(v.Tmpfs.Size)}
		}
	case types.VolumeTypeImage:
		if err := only("image"); err != nil {
			return mount.Mount{}, err
		}
		if v.Source == "" {
			return mount.Mount{}, fmt.Errorf("image mount without a source")
		}
		if v.Image != nil {
			m.ImageOptions = &mount.ImageOptions{Subpath: v.Image.SubPath}
		}
	default:
		return mount.Mount{}, fmt.Errorf("volume type %q is not volume, bind, tmpfs or image", v.Type)
	}
	return m, nil
}
