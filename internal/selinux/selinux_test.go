package selinux

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeInfo struct{ mode fs.FileMode }

func (f fakeInfo) Name() string       { return "restorecon" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

func TestRestore(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		present  map[string]fs.FileMode
		runErr   error
		paths    []string
		wantRun  []string
		wantWarn string
	}{
		{
			name:    "selinux disabled",
			present: map[string]fs.FileMode{"/usr/sbin/restorecon": 0o755},
			paths:   []string{"/usr/local/bin/dockmesh"},
		},
		{
			name:    "no paths",
			enabled: true,
			present: map[string]fs.FileMode{"/usr/sbin/restorecon": 0o755},
		},
		{
			name:    "usr sbin",
			enabled: true,
			present: map[string]fs.FileMode{"/usr/sbin/restorecon": 0o755, "/sbin/restorecon": 0o755},
			paths:   []string{"/usr/local/bin/dockmesh", "/usr/local/bin/dockmesh.prev"},
			wantRun: []string{"/usr/sbin/restorecon", "--", "/usr/local/bin/dockmesh", "/usr/local/bin/dockmesh.prev"},
		},
		{
			name:    "sbin only",
			enabled: true,
			present: map[string]fs.FileMode{"/sbin/restorecon": 0o755},
			paths:   []string{"/usr/local/bin/dockmesh-agent"},
			wantRun: []string{"/sbin/restorecon", "--", "/usr/local/bin/dockmesh-agent"},
		},
		{
			name:     "not a regular file",
			enabled:  true,
			present:  map[string]fs.FileMode{"/usr/sbin/restorecon": fs.ModeDir | 0o755},
			paths:    []string{"/usr/local/bin/dockmesh"},
			wantWarn: "restorecon was not found",
		},
		{
			name:     "restorecon missing",
			enabled:  true,
			paths:    []string{"/usr/local/bin/dockmesh"},
			wantWarn: "restorecon was not found",
		},
		{
			name:     "restorecon fails",
			enabled:  true,
			present:  map[string]fs.FileMode{"/usr/sbin/restorecon": 0o755},
			runErr:   errors.New("exit status 255"),
			paths:    []string{"/usr/local/bin/dockmesh"},
			wantRun:  []string{"/usr/sbin/restorecon", "--", "/usr/local/bin/dockmesh"},
			wantWarn: "reset SELinux label",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string
			r := relabeler{
				enabled: func() bool { return tt.enabled },
				stat: func(p string) (os.FileInfo, error) {
					if m, ok := tt.present[p]; ok {
						return fakeInfo{m}, nil
					}
					return nil, fs.ErrNotExist
				},
				run: func(_ context.Context, name string, args ...string) ([]byte, error) {
					ran = append([]string{name}, args...)
					return nil, tt.runErr
				},
			}
			var logs bytes.Buffer
			r.restore(slog.New(slog.NewTextHandler(&logs, nil)), tt.paths...)
			if !slices.Equal(ran, tt.wantRun) {
				t.Errorf("ran %q, want %q", ran, tt.wantRun)
			}
			if tt.wantWarn == "" && logs.Len() > 0 {
				t.Errorf("unexpected log: %s", logs.String())
			}
			if tt.wantWarn != "" && !strings.Contains(logs.String(), tt.wantWarn) {
				t.Errorf("log %q does not contain %q", logs.String(), tt.wantWarn)
			}
		})
	}
}
