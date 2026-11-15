package host

import (
	"context"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/moby/moby/api/types/volume"
)

type fakeVolumeInspector struct{ err error }

func (f fakeVolumeInspector) InspectVolume(context.Context, string) (volume.Volume, error) {
	return volume.Volume{}, f.err
}

func TestRequireRemoteVolume(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		missing bool
		fails   bool
	}{
		{"exists", nil, false, false},
		{"missing", errors.New("Error response from daemon: get pgdata: no such volume"), true, true},
		{"agent error", errors.New("agent offline"), false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := requireRemoteVolume(context.Background(), fakeVolumeInspector{tc.err}, "pgdata")
			var nf *docker.VolumeNotFoundError
			if got := errors.As(err, &nf); got != tc.missing {
				t.Fatalf("VolumeNotFoundError = %v, want %v (err %v)", got, tc.missing, err)
			}
			if (err != nil) != tc.fails {
				t.Fatalf("err = %v, want failure %v", err, tc.fails)
			}
		})
	}
}
