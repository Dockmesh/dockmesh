package handlers

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/host"
)

func TestMapBrowseStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "escape", err: host.ErrVolumePathEscape, want: http.StatusBadRequest},
		{name: "wrapped escape", err: fmt.Errorf("agent: %w", host.ErrVolumePathEscape), want: http.StatusBadRequest},
		{name: "too long", err: host.ErrVolumePathTooLong, want: http.StatusBadRequest},
		{name: "not a dir", err: host.ErrVolumeNotDir, want: http.StatusBadRequest},
		{name: "not a file", err: host.ErrVolumeNotFile, want: http.StatusBadRequest},
		{name: "no mountpoint", err: host.ErrVolumeMountpointMissing, want: http.StatusConflict},
		{name: "path error", err: &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}, want: http.StatusNotFound},
		{name: "wrapped not found", err: fmt.Errorf("x: %w", fs.ErrNotExist), want: http.StatusNotFound},
		{name: "other", err: errors.New("daemon down"), want: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapBrowseStatus(tt.err); got != tt.want {
				t.Fatalf("mapBrowseStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
