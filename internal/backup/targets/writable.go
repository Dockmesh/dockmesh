package targets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Writability of a local directory as the service sees it.
const (
	DirWritable = "writable"
	// DirReadOnly: the directory is on a read-only file system for the
	// service, which is what a directory missing from the unit's
	// ReadWritePaths looks like.
	DirReadOnly = "read_only"
	// DirNoPermission: the service account may not write there.
	DirNoPermission = "permission_denied"
	// DirMissing: the directory does not exist and the service can't
	// create it.
	DirMissing = "missing"
	DirError   = "error"
)

// DirCheck is the result of CheckDirWritable.
type DirCheck struct {
	Path  string `json:"path"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// InUse is set when a local target writes below Path.
	InUse bool `json:"in_use"`
}

// writeTestPrefix names the files the write checks create and remove.
const writeTestPrefix = ".dockmesh-write-test-"

// dirCheckTimeout bounds one directory check, so a hung network mount
// can't hold the request.
const dirCheckTimeout = 5 * time.Second

var (
	probesMu sync.Mutex
	probes   = map[string]bool{}
)

// CheckDirWritable creates and removes a file in dir to see whether the
// service can write there. A missing dir counts as writable when the
// service may create entries in its nearest existing parent, since local
// targets create it on first use; that parent is only asked, never
// written to, as it may lie outside every allowed root.
func CheckDirWritable(dir string) DirCheck {
	// A check stuck on a hung mount keeps its goroutine; later calls for
	// the same dir answer at once instead of starting another one.
	probesMu.Lock()
	if probes[dir] {
		probesMu.Unlock()
		return DirCheck{Path: dir, State: DirError, Error: "an earlier check of this directory has not returned"}
	}
	probes[dir] = true
	probesMu.Unlock()
	done := make(chan DirCheck, 1)
	go func() {
		c := checkDirWritable(dir)
		probesMu.Lock()
		delete(probes, dir)
		probesMu.Unlock()
		done <- c
	}()
	select {
	case c := <-done:
		return c
	case <-time.After(dirCheckTimeout):
		return DirCheck{Path: dir, State: DirError, Error: "timed out"}
	}
}

func checkDirWritable(dir string) DirCheck {
	out := DirCheck{Path: dir}
	probe := dir
	missing := false
	for {
		fi, err := os.Stat(probe)
		if err == nil {
			if !fi.IsDir() {
				out.State, out.Error = DirError, probe+" is not a directory"
				return out
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			out.State, out.Error = classifyWriteErr(err), err.Error()
			return out
		}
		missing = true
		parent := filepath.Dir(probe)
		if parent == probe {
			out.State, out.Error = DirMissing, err.Error()
			return out
		}
		probe = parent
	}
	if missing {
		if err := canCreateIn(probe); err != nil {
			out.State, out.Error = DirMissing, err.Error()
			return out
		}
		out.State = DirWritable
		return out
	}
	f, err := os.CreateTemp(probe, writeTestPrefix+"*")
	if err == nil {
		name := f.Name()
		err = f.Close()
		if rerr := os.Remove(name); err == nil {
			err = rerr
		}
	}
	if err != nil {
		out.State, out.Error = classifyWriteErr(err), err.Error()
		return out
	}
	out.State = DirWritable
	return out
}

func classifyWriteErr(err error) string {
	switch {
	case errors.Is(err, syscall.EROFS):
		return DirReadOnly
	case errors.Is(err, fs.ErrPermission):
		return DirNoPermission
	}
	return DirError
}

// IsReadOnly reports whether err comes from a read-only file system.
func IsReadOnly(err error) bool { return errors.Is(err, syscall.EROFS) }

// AllowedDirs returns the directories local targets may write to: the
// allowed roots and the directory an unset path means.
func AllowedDirs() []string {
	var out []string
	if localRoots != nil {
		out = append(out, localRoots()...)
	}
	if emptyPathDir != "" {
		covered := false
		for _, r := range out {
			if Within(r, emptyPathDir) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, emptyPathDir)
		}
	}
	return out
}

// ErrProbeNotDeleted reports a test file that was written but could not
// be deleted, as on targets whose credentials may not delete. Backups
// still work there; only pruning old runs doesn't.
var ErrProbeNotDeleted = errors.New("the test file could not be deleted")

// ProbeWrite writes a small file to t and deletes it again, so a target
// test fails where a backup run would.
func ProbeWrite(ctx context.Context, t Target) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	name := writeTestPrefix + hex.EncodeToString(rnd[:])
	w, err := t.Open(ctx, name)
	if err != nil {
		return fmt.Errorf("write a test file: %w", err)
	}
	_, err = io.WriteString(w, "dockmesh write test\n")
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if a, ok := w.(Aborter); ok {
			_ = a.Abort()
		}
		_ = t.Delete(ctx, name)
		return fmt.Errorf("write a test file: %w", err)
	}
	if err := t.Delete(ctx, name); err != nil {
		return fmt.Errorf("%w (%s): %w", ErrProbeNotDeleted, name, err)
	}
	return nil
}

// LocalDir returns the directory a stored local target config writes to.
func LocalDir(raw any) (string, error) {
	var cfg LocalConfig
	if err := decodeConfig(raw, &cfg); err != nil {
		return "", err
	}
	if cfg.Path == "" {
		if emptyPathDir == "" {
			return "", ErrLocalPathUnset
		}
		return emptyPathDir, nil
	}
	return filepath.Abs(cfg.Path)
}
