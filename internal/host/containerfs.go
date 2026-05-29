package host

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/docker/docker/api/types"
)

// Container-file browsing reuses the VolumeEntry / VolumeFileResult
// shapes from volumebrowse.go — listing one entry of a container
// filesystem is structurally identical to one entry of a volume.

var (
	// ErrContainerPathInvalid is returned when the requested path is
	// empty, relative, or otherwise rejected before hitting the daemon.
	ErrContainerPathInvalid = errors.New("container path must be absolute and non-empty")
	// ErrContainerNotDir / NotFile are the container counterparts of the
	// volume variants so handlers can map both via mapBrowseStatus.
	ErrContainerNotDir  = errors.New("requested container path is not a directory")
	ErrContainerNotFile = errors.New("requested container path is not a regular file")
)

// sanitizeContainerPath normalises and validates a caller-supplied
// path. The daemon will happily accept relative paths and interpret
// them against the container's root, but exposing that is confusing
// for operators — we require absolute paths. Trailing slashes are
// dropped (except for root) so "/etc/" and "/etc" behave the same.
func sanitizeContainerPath(p string) (string, error) {
	if p == "" {
		return "", ErrContainerPathInvalid
	}
	if !strings.HasPrefix(p, "/") {
		return "", ErrContainerPathInvalid
	}
	clean := path.Clean(p)
	if len(clean) > MaxBrowsePathLen {
		return "", ErrVolumePathTooLong
	}
	return clean, nil
}

// BrowseContainerDir lists the direct children of a path inside the
// container by asking the daemon for a tar of that path and walking
// the archive at depth 1. Works against scratch / distroless images
// because it doesn't rely on a shell inside the container.
func BrowseContainerDir(ctx context.Context, cli *docker.Client, id, p string) ([]VolumeEntry, error) {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return nil, err
	}
	stat, err := cli.ContainerStatPath(ctx, id, clean)
	if err != nil {
		return nil, err
	}
	if !stat.Mode.IsDir() {
		return nil, ErrContainerNotDir
	}

	rdr, _, err := cli.CopyFromContainer(ctx, id, clean)
	if err != nil {
		return nil, err
	}
	defer rdr.Close()

	// Docker tars the path as `<basename>/...` — e.g. CopyFromContainer
	// of "/etc" yields entries like "etc/", "etc/passwd", "etc/nginx/".
	// CopyFromContainer of "/" is special: the daemon emits entries with
	// a leading slash ("/", "/.dockerenv", "/bin/", "/bin/arch") rather
	// than a basename prefix. Strip that leading "/" so depth-1 children
	// fall through the same "no further slash" filter below.
	var prefix string
	if clean == "/" {
		prefix = "/"
	} else {
		prefix = path.Base(clean) + "/"
	}

	tr := tar.NewReader(rdr)
	var out []VolumeEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(hdr.Name, prefix)
		name = strings.TrimSuffix(name, "/")
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		out = append(out, tarHeaderToEntry(name, hdr))
	}
	return out, nil
}

func tarHeaderToEntry(name string, hdr *tar.Header) VolumeEntry {
	e := VolumeEntry{
		Name:    name,
		Size:    hdr.Size,
		Mode:    fs.FileMode(hdr.Mode).Perm().String(),
		ModTime: hdr.ModTime,
	}
	switch hdr.Typeflag {
	case tar.TypeSymlink, tar.TypeLink:
		e.Type = "symlink"
		e.LinkDest = hdr.Linkname
	case tar.TypeDir:
		e.Type = "dir"
	default:
		e.Type = "file"
	}
	return e
}

// ReadContainerFile copies a single file out of the container and
// returns the first maxBytes of its content along with a binary flag.
// Stat is consulted first so we can reject "this is a directory" with
// a clean error before opening the (potentially huge) tar stream.
func ReadContainerFile(ctx context.Context, cli *docker.Client, id, p string, maxBytes int64) (*VolumeFileResult, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return nil, err
	}
	stat, err := cli.ContainerStatPath(ctx, id, clean)
	if err != nil {
		return nil, err
	}
	if stat.Mode.IsDir() {
		return nil, ErrContainerNotFile
	}

	rdr, _, err := cli.CopyFromContainer(ctx, id, clean)
	if err != nil {
		return nil, err
	}
	defer rdr.Close()

	tr := tar.NewReader(rdr)
	hdr, err := tr.Next()
	if err != nil {
		return nil, err
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
		return nil, ErrContainerNotFile
	}

	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(tr, buf)
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// All data read — n is correct.
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
		Size:      hdr.Size,
		Truncated: truncated,
		Binary:    looksBinary(content),
	}, nil
}

// DownloadContainerFile streams a file's raw bytes out of the container
// for the caller to write directly to the HTTP response — used by the
// "Download" button so large files don't have to fit in memory.
// Returns the tar reader positioned past the header so the caller can
// io.Copy it; caller closes the underlying ReadCloser.
func DownloadContainerFile(ctx context.Context, cli *docker.Client, id, p string) (io.ReadCloser, *tar.Header, error) {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return nil, nil, err
	}
	stat, err := cli.ContainerStatPath(ctx, id, clean)
	if err != nil {
		return nil, nil, err
	}
	if stat.Mode.IsDir() {
		return nil, nil, ErrContainerNotFile
	}
	rdr, _, err := cli.CopyFromContainer(ctx, id, clean)
	if err != nil {
		return nil, nil, err
	}
	tr := tar.NewReader(rdr)
	hdr, err := tr.Next()
	if err != nil {
		rdr.Close()
		return nil, nil, err
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
		rdr.Close()
		return nil, nil, ErrContainerNotFile
	}
	return &tarBodyReader{rdr: rdr, tr: tr}, hdr, nil
}

// tarBodyReader exposes the current tar entry's body as a ReadCloser
// that, on Close, also closes the underlying CopyFromContainer stream.
type tarBodyReader struct {
	rdr io.ReadCloser
	tr  *tar.Reader
}

func (t *tarBodyReader) Read(p []byte) (int, error) { return t.tr.Read(p) }
func (t *tarBodyReader) Close() error               { return t.rdr.Close() }

// WriteContainerFile uploads a single file into the container at the
// given path. The path's parent must already exist (we don't auto-
// mkdir — that's a separate concern). Mode + modtime are stamped on
// the tar header so the file shows up with sensible perms inside.
func WriteContainerFile(ctx context.Context, cli *docker.Client, id, p string, data []byte, mode int64) error {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}

	dir := path.Dir(clean)
	name := path.Base(clean)

	// Verify the parent dir exists and is a directory — fails fast with
	// a clear error rather than letting docker silently no-op.
	if dir != "/" {
		stat, err := cli.ContainerStatPath(ctx, id, dir)
		if err != nil {
			return err
		}
		if !stat.Mode.IsDir() {
			return ErrContainerNotDir
		}
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return cli.CopyToContainer(ctx, id, dir, &buf)
}

// Ensure types import survives even if the only reference is via
// docker.Client's exported methods.
var _ types.ContainerPathStat
