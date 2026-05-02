package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/stacks"
	dvolume "github.com/docker/docker/api/types/volume"
)

// stackRestoreFileSizeCap protects against a malicious or corrupted
// archive trying to write huge files into the stacks directory. 32 MiB
// per file is generous for compose / .env / supplemental config files
// (the original tar wrote them straight from disk, so anything larger
// than this should never have been there in the first place).
const stackRestoreFileSizeCap = 32 * 1024 * 1024

// stackRestoreMaxEntries caps the number of tar entries we'll process,
// guarding against zip-bomb-style inflation.
const stackRestoreMaxEntries = 50_000

// restoreStackInto extracts a stack-type backup tarball into the named
// stack. Layout produced by tarStackWithVolumes:
//
//	stack/<rel>            → /stacks/<name>/<rel>
//	volumes/<vol>.tar.gz   → docker volume <vol>, untarred via the same
//	                         helper container the volume backup uses.
//
// The compose file content is captured during the walk and Update'd via
// the manager at the end so the in-memory cache and fsnotify watchers
// see a consistent post-restore state. Volumes are recreated on demand
// (VolumeCreate is idempotent — already-present volumes are reused).
//
// Safety:
//   - Path traversal: the relative path inside the tar is cleaned and
//     rejected if it contains "..".
//   - Symlinks inside the archive are skipped with a warning rather
//     than followed.
//   - The destination stack name comes from the caller, not the tar —
//     restore-as-other-name is supported.
func (s *Service) restoreStackInto(ctx context.Context, stackName string, src io.Reader, report *StackRestoreReport) error {
	if err := stacks.ValidateName(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	if s.stacks == nil {
		return errors.New("stacks manager not configured")
	}

	stackDir, err := s.stacks.Dir(stackName)
	if err != nil {
		return fmt.Errorf("resolve stack dir: %w", err)
	}
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		return fmt.Errorf("create stack dir: %w", err)
	}

	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)

	// Compose + env extracted from the archive — used to call manager
	// Update at the end so the cache stays consistent.
	var composeContent, envContent string

	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		entries++
		if entries > stackRestoreMaxEntries {
			return fmt.Errorf("archive has too many entries (>%d)", stackRestoreMaxEntries)
		}

		name := filepath.ToSlash(hdr.Name)
		switch {
		case strings.HasPrefix(name, "stack/"):
			rel := strings.TrimPrefix(name, "stack/")
			if rel == "" {
				continue
			}
			if !safeRelPath(rel) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("skipped path traversal attempt: %s", name))
				continue
			}
			dst := filepath.Join(stackDir, rel)
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(dst, 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", rel, err)
				}
			case tar.TypeReg, tar.TypeRegA:
				if hdr.Size > stackRestoreFileSizeCap {
					report.Warnings = append(report.Warnings, fmt.Sprintf("skipped oversize file (%d bytes): %s", hdr.Size, rel))
					continue
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return fmt.Errorf("mkdir parent for %s: %w", rel, err)
				}
				buf, err := io.ReadAll(io.LimitReader(tr, stackRestoreFileSizeCap+1))
				if err != nil {
					return fmt.Errorf("read %s: %w", rel, err)
				}
				if int64(len(buf)) > stackRestoreFileSizeCap {
					report.Warnings = append(report.Warnings, fmt.Sprintf("skipped oversize file: %s", rel))
					continue
				}
				if err := os.WriteFile(dst, buf, 0o644); err != nil {
					return fmt.Errorf("write %s: %w", rel, err)
				}
				report.FilesRestored = append(report.FilesRestored, rel)
				switch rel {
				case "compose.yaml":
					composeContent = string(buf)
				case ".env":
					envContent = string(buf)
				}
			case tar.TypeSymlink, tar.TypeLink:
				report.Warnings = append(report.Warnings, fmt.Sprintf("skipped symlink: %s", rel))
			default:
				// Unknown / device / fifo entries — silently skip,
				// they have no place in a stack dir.
			}

		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".tar.gz"):
			volName := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".tar.gz")
			if volName == "" {
				report.Warnings = append(report.Warnings, fmt.Sprintf("skipped malformed volume entry: %s", name))
				continue
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				report.Warnings = append(report.Warnings, fmt.Sprintf("skipped non-regular volume entry: %s", name))
				continue
			}
			if err := s.restoreOneVolume(ctx, volName, tr, hdr.Size); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("volume %s restore failed: %v", volName, err))
				continue
			}
			report.VolumesRestored = append(report.VolumesRestored, volName)

		default:
			// Ignore unexpected top-level entries silently. Tarballs
			// from older versions might carry stray files.
		}
	}

	if composeContent == "" {
		return errors.New("archive does not contain stack/compose.yaml — refusing to mark a stack record without a compose file")
	}

	// Make sure the stacks manager picks up the restored compose. Update
	// rewrites the file (idempotent — same bytes) and refreshes the
	// in-memory cache + watcher so /stacks list reflects the restore.
	if _, err := s.stacks.Update(stackName, composeContent, envContent); err != nil {
		return fmt.Errorf("register restored stack with manager: %w", err)
	}
	slog.Info("stack restored from backup",
		"stack", stackName,
		"files", len(report.FilesRestored),
		"volumes", len(report.VolumesRestored),
		"warnings", len(report.Warnings),
	)
	return nil
}

// restoreOneVolume buffers a single volumes/<name>.tar.gz entry to a
// temp file (untarVolume needs an io.Reader it can rewind, and the
// outer tar's Reader can't be split mid-stream) then untars it into
// the named docker volume, creating it if needed. Idempotent: an
// existing volume's contents are overwritten, which is what restore
// is supposed to do.
func (s *Service) restoreOneVolume(ctx context.Context, volName string, src io.Reader, size int64) error {
	if s.docker == nil || !s.docker.Connected() {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	// Idempotent create — VolumeCreate succeeds if the volume already
	// exists (Docker treats it as a get-or-create).
	if _, err := cli.VolumeCreate(ctx, dvolume.CreateOptions{Name: volName}); err != nil {
		return fmt.Errorf("ensure volume %s: %w", volName, err)
	}

	tmp, err := os.CreateTemp("", "dm-restore-vol-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.CopyN(tmp, src, size); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("buffer volume tar: %w", err)
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	return untarVolume(ctx, s.docker, volName, tmp)
}

// safeRelPath rejects paths that try to escape the stack directory via
// .. or absolute prefixes. Only "stack/<simple-rel>" entries are
// accepted; the caller has already trimmed "stack/" before calling.
func safeRelPath(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return false
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." {
		return false
	}
	if strings.HasPrefix(cleaned, "..") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
