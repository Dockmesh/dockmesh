package host

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"github.com/dockmesh/dockmesh/internal/docker"
)

// Container-file browsing reuses the VolumeEntry / VolumeFileResult
// shapes from volumebrowse.go — listing one entry of a container
// filesystem is structurally identical to one entry of a volume.
//
// The same functions serve the server's own daemon and, on the agent,
// the agent host's daemon, so both enforce the same rules.

var (
	// ErrContainerPathInvalid is returned when the requested path is
	// empty, relative, or otherwise rejected before hitting the daemon.
	ErrContainerPathInvalid = errors.New("container path must be absolute and non-empty")
	// ErrContainerNotDir / NotFile are the container counterparts of the
	// volume variants so handlers can map both via mapBrowseStatus.
	ErrContainerNotDir  = errors.New("requested container path is not a directory")
	ErrContainerNotFile = errors.New("requested container path is not a regular file")
	// ErrContainerPathDenied covers the kernel filesystems and special
	// files: /proc/<pid>/environ alone would hand out every environment
	// secret, which needs a different permission.
	ErrContainerPathDenied = errors.New("/proc, /sys, /dev and device files are not accessible")
	// ErrContainerSymlink is returned for writes and deletes aimed at a
	// symbolic link's target through the link.
	ErrContainerSymlink = errors.New("requested container path is a symbolic link")
	// ErrContainerFileTooLarge is returned for uploads above
	// MaxContainerUploadBytes.
	ErrContainerFileTooLarge = fmt.Errorf("file is larger than the %d MiB upload limit", MaxContainerUploadBytes>>20)
	// ErrContainerDirTooLarge is returned for directories with more than
	// maxContainerDirEntries direct children.
	ErrContainerDirTooLarge = fmt.Errorf("directory has more than %d entries", maxContainerDirEntries)
	// ErrContainerNoRm is returned when a delete can't run rm in the
	// container (stopped, or an image without rm such as scratch).
	ErrContainerNoRm = errors.New("deleting needs a running container with an rm command")
)

const (
	// MaxContainerPreviewBytes is the most a preview read returns; larger
	// files are downloaded instead.
	MaxContainerPreviewBytes = 1 << 20
	// MaxContainerUploadBytes caps one upload or save. It keeps a write,
	// base64-encoded, well inside one agent frame.
	MaxContainerUploadBytes = 8 << 20
	maxContainerDirEntries  = 10000
	maxContainerPathDepth   = 64
)

// deniedContainerRoots are refused wherever a path resolves to.
var deniedContainerRoots = []string{"/proc", "/sys", "/dev"}

func deniedContainerPath(p string) bool {
	for _, d := range deniedContainerRoots {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func specialFile(m fs.FileMode) bool {
	return m&(fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeIrregular) != 0
}

// sanitizeContainerPath normalises and validates a caller-supplied
// path. The daemon will happily accept relative paths and interpret
// them against the container's root, but exposing that is confusing
// for operators — we require absolute paths. Trailing slashes are
// dropped (except for root) so "/etc/" and "/etc" behave the same.
func sanitizeContainerPath(p string) (string, error) {
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", ErrContainerPathInvalid
	}
	if len(p) > MaxBrowsePathLen {
		return "", ErrVolumePathTooLong
	}
	clean := path.Clean(p)
	if strings.Count(clean, "/") > maxContainerPathDepth {
		return "", ErrVolumePathTooLong
	}
	return clean, nil
}

// statErr turns the daemon's not-found into fs.ErrNotExist so handlers
// answer 404 for local and agent hosts alike.
func statErr(err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	}
	return err
}

// resolveContainerDir resolves every component of an absolute, clean
// path inside the container, following symlinks. The daemon evaluates a
// link in the scope of the container's root filesystem and reports the
// result as LinkTarget, so the walk never leaves that root; each step is
// also checked against the denied areas, so a link into /proc is refused
// the same as the path itself.
func resolveContainerDir(ctx context.Context, cli client.ContainerAPIClient, id, clean string) (string, container.PathStat, error) {
	cur := "/"
	st, err := cli.ContainerStatPath(ctx, id, cur)
	if err != nil {
		return "", st, statErr(err)
	}
	for _, comp := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if comp == "" {
			continue
		}
		next := path.Join(cur, comp)
		if deniedContainerPath(next) {
			return "", st, ErrContainerPathDenied
		}
		st, err = cli.ContainerStatPath(ctx, id, next)
		if err != nil {
			return "", st, statErr(err)
		}
		if st.Mode&fs.ModeSymlink != 0 {
			target, err := linkTarget(st)
			if err != nil {
				return "", st, err
			}
			if deniedContainerPath(target) {
				return "", st, ErrContainerPathDenied
			}
			next = target
			st, err = cli.ContainerStatPath(ctx, id, next)
			if err != nil {
				return "", st, statErr(err)
			}
			// The daemon resolved the whole chain; a link is still a
			// link only when the chain is broken or loops.
			if st.Mode&fs.ModeSymlink != 0 {
				return "", st, fmt.Errorf("%w: unresolvable symbolic link", fs.ErrNotExist)
			}
		}
		cur = next
	}
	if specialFile(st.Mode) {
		return "", st, ErrContainerPathDenied
	}
	return cur, st, nil
}

// linkTarget is the daemon's fully resolved, absolute target of a link.
func linkTarget(st container.PathStat) (string, error) {
	t := st.LinkTarget
	if t == "" || !strings.HasPrefix(t, "/") || path.Clean(t) != t {
		return "", ErrContainerPathDenied
	}
	return t, nil
}

// resolveContainerParent resolves the directory of clean and stats the
// last element itself without following it. exists is false when the
// last element doesn't exist yet.
func resolveContainerParent(ctx context.Context, cli client.ContainerAPIClient, id, clean string) (dir, name string, st container.PathStat, exists bool, err error) {
	if clean == "/" {
		return "", "", st, false, ErrContainerNotFile
	}
	dir, dst, err := resolveContainerDir(ctx, cli, id, path.Dir(clean))
	if err != nil {
		return "", "", st, false, err
	}
	if !dst.Mode.IsDir() {
		return "", "", st, false, ErrContainerNotDir
	}
	name = path.Base(clean)
	full := path.Join(dir, name)
	if deniedContainerPath(full) {
		return "", "", st, false, ErrContainerPathDenied
	}
	st, err = cli.ContainerStatPath(ctx, id, full)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return dir, name, st, false, nil
		}
		return "", "", st, false, err
	}
	if specialFile(st.Mode) {
		return "", "", st, false, ErrContainerPathDenied
	}
	return dir, name, st, true, nil
}

// BrowseContainerDir lists the direct children of a path inside the
// container by asking the daemon for a tar of that path and walking
// the archive at depth 1. Works against scratch / distroless images
// because it doesn't rely on a shell inside the container.
func BrowseContainerDir(ctx context.Context, cli client.ContainerAPIClient, id, p string) ([]VolumeEntry, error) {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return nil, err
	}
	resolved, stat, err := resolveContainerDir(ctx, cli, id, clean)
	if err != nil {
		return nil, err
	}
	if !stat.Mode.IsDir() {
		return nil, ErrContainerNotDir
	}

	rdr, _, err := cli.CopyFromContainer(ctx, id, resolved)
	if err != nil {
		return nil, statErr(err)
	}
	defer rdr.Close()

	// Docker tars the path as `<basename>/...` — e.g. CopyFromContainer
	// of "/etc" yields entries like "etc/", "etc/passwd", "etc/nginx/".
	// CopyFromContainer of "/" is special: the daemon emits entries with
	// a leading slash ("/", "/.dockerenv", "/bin/", "/bin/arch") rather
	// than a basename prefix. Strip that leading "/" so depth-1 children
	// fall through the same "no further slash" filter below.
	var prefix string
	if resolved == "/" {
		prefix = "/"
	} else {
		prefix = path.Base(resolved) + "/"
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
		if len(out) == maxContainerDirEntries {
			return nil, ErrContainerDirTooLarge
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

// openContainerFile resolves p to a regular file and opens its content.
func openContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string) (io.ReadCloser, *tar.Header, error) {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return nil, nil, err
	}
	resolved, stat, err := resolveContainerDir(ctx, cli, id, clean)
	if err != nil {
		return nil, nil, err
	}
	if !stat.Mode.IsRegular() {
		return nil, nil, ErrContainerNotFile
	}
	rdr, _, err := cli.CopyFromContainer(ctx, id, resolved)
	if err != nil {
		return nil, nil, statErr(err)
	}
	tr := tar.NewReader(rdr)
	hdr, err := tr.Next()
	if err != nil {
		rdr.Close()
		return nil, nil, err
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA { //nolint:staticcheck // older daemons still write TypeRegA
		rdr.Close()
		return nil, nil, ErrContainerNotFile
	}
	return &tarBodyReader{rdr: rdr, tr: tr}, hdr, nil
}

// ReadContainerFile returns the first maxBytes (at most
// MaxContainerPreviewBytes) of a file along with a binary flag.
func ReadContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string, maxBytes int64) (*VolumeFileResult, error) {
	if maxBytes <= 0 || maxBytes > MaxContainerPreviewBytes {
		maxBytes = MaxContainerPreviewBytes
	}
	body, hdr, err := openContainerFile(ctx, cli, id, p)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(body, buf)
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

// ContainerFileStat describes a file about to be downloaded.
type ContainerFileStat struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// StatContainerFile checks that p is a readable regular file and returns
// its name and size.
func StatContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string) (*ContainerFileStat, error) {
	body, hdr, err := openContainerFile(ctx, cli, id, p)
	if err != nil {
		return nil, err
	}
	body.Close()
	clean, _ := sanitizeContainerPath(p)
	return &ContainerFileStat{Name: path.Base(clean), Size: hdr.Size}, nil
}

// DownloadContainerFile streams a file's raw bytes out of the container
// for the caller to write directly to the HTTP response — used by the
// "Download" button so large files don't have to fit in memory. The
// caller closes the returned reader.
func DownloadContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string) (io.ReadCloser, *tar.Header, error) {
	return openContainerFile(ctx, cli, id, p)
}

// tarBodyReader exposes the current tar entry's body as a ReadCloser
// that, on Close, also closes the underlying CopyFromContainer stream.
type tarBodyReader struct {
	rdr io.ReadCloser
	tr  *tar.Reader
}

func (t *tarBodyReader) Read(p []byte) (int, error) { return t.tr.Read(p) }
func (t *tarBodyReader) Close() error               { return t.rdr.Close() }

// WriteContainerFile creates or replaces a regular file at p. The parent
// must already exist. A replaced file keeps its permissions unless mode
// is set; a new one defaults to 0644.
func WriteContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string, data []byte, mode int64) error {
	if len(data) > MaxContainerUploadBytes {
		return ErrContainerFileTooLarge
	}
	if mode < 0 || mode > 0o7777 {
		return errors.New("mode must be between 0 and 07777")
	}
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return err
	}
	dir, name, st, exists, err := resolveContainerParent(ctx, cli, id, clean)
	if err != nil {
		return err
	}
	if exists {
		switch {
		case st.Mode&fs.ModeSymlink != 0:
			return ErrContainerSymlink
		case !st.Mode.IsRegular():
			return ErrContainerNotFile
		}
		if mode == 0 {
			mode = int64(st.Mode.Perm())
		}
	}
	if mode == 0 {
		mode = 0o644
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
	return statErr(cli.CopyToContainer(ctx, id, dir, &buf, container.CopyToContainerOptions{}))
}

// DeleteContainerFile removes a file or symbolic link (never a
// directory). The archive API can't delete, so rm runs in the container
// with the resolved path as a plain argument, no shell.
func DeleteContainerFile(ctx context.Context, cli client.ContainerAPIClient, id, p string) error {
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return err
	}
	dir, name, st, exists, err := resolveContainerParent(ctx, cli, id, clean)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s", fs.ErrNotExist, clean)
	}
	if !st.Mode.IsRegular() && st.Mode&fs.ModeSymlink == 0 {
		return ErrContainerNotFile
	}
	out, code, err := docker.RunExec(ctx, cli, id, []string{"rm", "--", path.Join(dir, name)}, 4<<10)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrContainerNoRm, err)
	}
	if code != 0 {
		msg := strings.TrimSpace(string(out))
		if code == 126 || code == 127 {
			return ErrContainerNoRm
		}
		return fmt.Errorf("rm exited with %d: %s", code, msg)
	}
	return nil
}
