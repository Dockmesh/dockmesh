package targets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCheckDirWritable(t *testing.T) {
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		dir    string
		state  string
		asRoot bool // root writes anywhere, so the case can't fail there
	}{
		{name: "existing", dir: base, state: DirWritable, asRoot: true},
		{name: "missing below a writable parent", dir: filepath.Join(base, "a", "b"), state: DirWritable, asRoot: true},
		{name: "no permission", dir: locked, state: DirNoPermission},
		{name: "missing below a parent it can't write", dir: filepath.Join(locked, "x"), state: DirMissing},
		{name: "not a directory", dir: file, state: DirError, asRoot: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Geteuid() == 0 && !tt.asRoot {
				t.Skip("root ignores directory permissions")
			}
			got := CheckDirWritable(tt.dir)
			if got.State != tt.state || got.Path != tt.dir {
				t.Fatalf("CheckDirWritable(%s) = %+v, want %s", tt.dir, got, tt.state)
			}
			left, _ := filepath.Glob(filepath.Join(base, writeTestPrefix+"*"))
			if len(left) > 0 {
				t.Fatalf("test files left: %v", left)
			}
			if _, err := os.Stat(filepath.Join(base, "a")); err == nil {
				t.Fatal("a missing directory was created")
			}
		})
	}
}

func TestClassifyWriteErr(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "open", Path: "/x", Err: syscall.EROFS}, DirReadOnly},
		{fmt.Errorf("write: %w", &os.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}), DirNoPermission},
		{errors.New("other"), DirError},
	}
	for _, tt := range tests {
		if got := classifyWriteErr(tt.err); got != tt.want {
			t.Errorf("classifyWriteErr(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}

type probeTarget struct {
	openErr, closeErr, deleteErr error
	written                      map[string]*bytes.Buffer
	deleted                      []string
}

type probeWriter struct {
	*bytes.Buffer
	err error
}

func (w probeWriter) Close() error { return w.err }

func (p *probeTarget) Open(_ context.Context, path string) (io.WriteCloser, error) {
	if p.openErr != nil {
		return nil, p.openErr
	}
	b := &bytes.Buffer{}
	p.written[path] = b
	return probeWriter{b, p.closeErr}, nil
}
func (p *probeTarget) Read(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("no")
}
func (p *probeTarget) List(context.Context, string) ([]Entry, error) { return nil, nil }
func (p *probeTarget) Delete(_ context.Context, path string) error {
	p.deleted = append(p.deleted, path)
	return p.deleteErr
}

func TestProbeWrite(t *testing.T) {
	rofs := &os.PathError{Op: "openat", Path: "x", Err: syscall.EROFS}
	tests := []struct {
		name       string
		tgt        *probeTarget
		wantErr    string
		readOnly   bool
		wantDelete bool
	}{
		{name: "writes and deletes", tgt: &probeTarget{}, wantDelete: true},
		{name: "read-only directory", tgt: &probeTarget{openErr: rofs}, wantErr: "read-only", readOnly: true},
		{name: "upload fails on close", tgt: &probeTarget{closeErr: errors.New("403 forbidden")}, wantErr: "403", wantDelete: true},
		{name: "can't delete", tgt: &probeTarget{deleteErr: errors.New("denied")}, wantErr: "delete the test file", wantDelete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.tgt.written = map[string]*bytes.Buffer{}
			err := ProbeWrite(context.Background(), tt.tgt)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ProbeWrite = %v, want %q", err, tt.wantErr)
			}
			if IsReadOnly(err) != tt.readOnly {
				t.Fatalf("IsReadOnly(%v) = %v", err, !tt.readOnly)
			}
			for name := range tt.tgt.written {
				if !strings.HasPrefix(name, writeTestPrefix) {
					t.Fatalf("probe wrote %q", name)
				}
			}
			if tt.wantDelete != (len(tt.tgt.deleted) == 1) {
				t.Fatalf("deleted %v", tt.tgt.deleted)
			}
		})
	}
}

func TestAllowedDirs(t *testing.T) {
	oldRoots, oldEmpty := localRoots, emptyPathDir
	t.Cleanup(func() { localRoots, emptyPathDir = oldRoots, oldEmpty })
	SetLocalRoots(func() []string { return []string{"/srv/backups", "/var/lib/dockmesh/data/backups"} })
	SetEmptyPathDir("/var/lib/dockmesh/data/backups")
	if got := AllowedDirs(); len(got) != 2 {
		t.Fatalf("AllowedDirs = %v, want the covered empty-path dir once", got)
	}
	SetEmptyPathDir("/opt/old/data/backups")
	if got := AllowedDirs(); len(got) != 3 || got[2] != "/opt/old/data/backups" {
		t.Fatalf("AllowedDirs = %v", got)
	}
}
