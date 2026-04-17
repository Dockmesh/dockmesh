package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VolumeEntry is one row in a directory listing. The size / modified
// time come straight from fs stat so "Size" on a directory is the
// directory inode's own size, not the cumulative content size.
type VolumeEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"` // "file" | "dir" | "symlink"
	Size     int64     `json:"size"`
	Mode     string    `json:"mode"` // "-rw-r--r--" style string
	ModTime  time.Time `json:"mod_time"`
	LinkDest string    `json:"link_dest,omitempty"`
}

// VolumeFileResult is the read-file response — content capped at the
// caller's requested maxBytes. Truncated = true signals "there's more;
// use the stream endpoint if you need the whole file".
type VolumeFileResult struct {
	Content   []byte `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// Common browse errors.
var (
	ErrVolumeMountpointMissing = errors.New("volume has no mountpoint (driver may not expose a host path)")
	ErrVolumePathEscape        = errors.New("requested path escapes volume root")
	ErrVolumeNotDir            = errors.New("requested path is not a directory")
	ErrVolumeNotFile           = errors.New("requested path is not a regular file")
	ErrVolumePathTooLong       = errors.New("requested path exceeds maximum length")
)

// MaxBrowsePathLen caps the sub-path length defensively — any real
// docker volume path is well under this, anything longer is either
// a typo or an attempt to DOS fs syscalls.
const MaxBrowsePathLen = 2048

// SanitizeVolumePath joins mountpoint + sub and rejects any result
// whose absolute path sits outside the mountpoint. The leading "/"
// on sub is stripped (absolute paths are treated as relative to the
// volume root), but ".." segments are NOT collapsed before the join
// — that lets filepath.Join evaluate them correctly and lets us
// detect escape attempts. Use for BOTH list and read-file.
func SanitizeVolumePath(mountpoint, sub string) (string, error) {
	if mountpoint == "" {
		return "", ErrVolumeMountpointMissing
	}
	if len(sub) > MaxBrowsePathLen {
		return "", ErrVolumePathTooLong
	}
	// Treat a leading "/" as "relative to the volume root" rather than
	// absolute-replace, but don't pre-clean — we need filepath.Join to
	// evaluate ".." *relative to the mountpoint* so escapes end up
	// outside absMount and get caught by the prefix check below.
	rel := strings.TrimPrefix(sub, "/")
	full := filepath.Join(mountpoint, rel)
	absMount, err := filepath.Abs(mountpoint)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	// Compare with a trailing separator so "/foo" doesn't accept
	// "/foo-escape".
	if absFull != absMount && !strings.HasPrefix(absFull, absMount+string(filepath.Separator)) {
		return "", ErrVolumePathEscape
	}
	return absFull, nil
}

// BrowseDir lists a directory's direct children. Caller is responsible
// for sanitizing the path before calling.
func BrowseDir(absPath string) ([]VolumeEntry, error) {
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrVolumeNotDir
	}
	dirents, err := os.ReadDir(absPath)
	if err != nil {
		return nil, err
	}
	out := make([]VolumeEntry, 0, len(dirents))
	for _, d := range dirents {
		fi, err := d.Info()
		if err != nil {
			// Stat may fail for dangling symlinks or racing deletes; skip.
			continue
		}
		entry := VolumeEntry{
			Name:    d.Name(),
			Size:    fi.Size(),
			Mode:    fi.Mode().Perm().String(),
			ModTime: fi.ModTime(),
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			entry.Type = "symlink"
			if dest, err := os.Readlink(filepath.Join(absPath, d.Name())); err == nil {
				entry.LinkDest = dest
			}
		case fi.IsDir():
			entry.Type = "dir"
		default:
			entry.Type = "file"
		}
		out = append(out, entry)
	}
	return out, nil
}

// ReadFile opens a regular file, reads up to maxBytes, and reports
// whether more data exists. A nil / zero maxBytes defaults to 1MiB.
func ReadFile(absPath string, maxBytes int64) (*VolumeFileResult, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Follow one level — many config files in volumes are symlinked.
		if info, err = os.Stat(absPath); err != nil {
			return nil, err
		}
	}
	if !info.Mode().IsRegular() {
		return nil, ErrVolumeNotFile
	}
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, maxBytes+1) // +1 so we can detect "there's more"
	n, err := io.ReadFull(f, buf)
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// All good — n is how many bytes we got.
	case err != nil:
		return nil, err
	}
	truncated := int64(n) > maxBytes
	if truncated {
		n = int(maxBytes)
	}
	content := buf[:n]
	return &VolumeFileResult{
		Content:   content,
		Size:      info.Size(),
		Truncated: truncated,
		Binary:    looksBinary(content),
	}, nil
}

// looksBinary is the same null-byte heuristic git uses: if the first
// 8 KiB contain a NUL, call it binary. Good enough for "should we show
// a text preview or a download button".
func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// ExtractMountpoint pulls the host-filesystem path out of a docker
// volume inspect response. Centralised here because RemoteHost (via
// the agent) and LocalHost both hit the same case.
func ExtractMountpoint(mp string) (string, error) {
	mp = strings.TrimSpace(mp)
	if mp == "" {
		return "", ErrVolumeMountpointMissing
	}
	return mp, nil
}

// Helper used by the agent's response builder — not strictly needed
// by the server, but symmetrical to the browse helpers above.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
