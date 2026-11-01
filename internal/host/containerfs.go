package host

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
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
	// ErrContainerPathDenied covers the kernel filesystems and special
	// files: /proc/<pid>/environ alone would hand out every environment
	// secret, which needs a different permission.
	ErrContainerPathDenied = errors.New("/proc, /sys, /dev and device files are not accessible")
	// ErrContainerSymlink is returned for writes aimed at a symbolic link.
	ErrContainerSymlink = errors.New("requested container path is a symbolic link")
	// ErrContainerFileTooLarge is returned for uploads above
	// MaxContainerUploadBytes.
	ErrContainerFileTooLarge = fmt.Errorf("file is larger than the %d MiB upload limit", MaxContainerUploadBytes>>20)
	// ErrContainerDirTooLarge is returned for directories with more than
	// maxContainerDirEntries direct children or maxBrowseTarBytes below it.
	ErrContainerDirTooLarge = fmt.Errorf("directory is too large to list (more than %d entries or %d MiB below it)", maxContainerDirEntries, maxBrowseTarBytes>>20)
	// ErrContainerNoRm is returned when a delete can't run rm in the
	// container (stopped, or an image without rm such as scratch).
	ErrContainerNoRm        = errors.New("deleting needs a running container with an rm command")
	ErrContainerModeInvalid = errors.New("mode must be between 0 and 0777")
	// ErrContainerFilesRefused is returned for containers whose files
	// would reach the Docker daemon or Dockmesh itself.
	ErrContainerFilesRefused = errors.New("files of Dockmesh's own containers and of containers with the Docker socket mounted are not accessible")
	// ErrContainerMountBelow is returned for a listing that would tar a
	// mount of the host's /proc, /sys, /dev or of a directory containing
	// them.
	ErrContainerMountBelow = errors.New("this directory contains a mount of the host's /proc, /sys or /dev; open a subdirectory instead")
)

const (
	// MaxContainerPreviewBytes is the most a preview read returns; larger
	// files are downloaded instead.
	MaxContainerPreviewBytes = 1 << 20
	// MaxContainerUploadBytes caps one upload or save. It keeps a write,
	// base64-encoded, well inside one agent frame.
	MaxContainerUploadBytes = 8 << 20
	maxContainerDirEntries  = 10000
	maxContainerPathDepth   = 32
	maxBrowseTarBytes       = 256 << 20
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

// containerFS is one container's file access: its mounts, so a path can
// be judged by where it lands on the host, not only by its name inside
// the container.
type containerFS struct {
	cli    client.ContainerAPIClient
	id     string
	mounts []container.MountPoint
}

// openContainerFS inspects the container and refuses Dockmesh's own
// helper containers and containers with the Docker socket mounted: files
// there reach the daemon itself, which is the host, or hold Dockmesh's
// keys and database.
func openContainerFS(ctx context.Context, cli client.ContainerAPIClient, id string) (*containerFS, error) {
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, statErr(err)
	}
	if info.Config != nil && info.Config.Labels["dockmesh.component"] != "" {
		return nil, ErrContainerFilesRefused
	}
	for _, m := range info.Mounts {
		if runtimeSocket(m.Source) || runtimeSocket(m.Destination) {
			return nil, ErrContainerFilesRefused
		}
	}
	f := &containerFS{cli: cli, id: id, mounts: info.Mounts}
	// A destination below a symlink in the image is mounted where the
	// link points, so compare paths against where each mount really is.
	for i, m := range f.mounts {
		if real, err := f.follow(ctx, m.Destination); err == nil {
			f.mounts[i].Destination = real
		}
	}
	return f, nil
}

// runtimeSocket reports whether a mount path is a container runtime's
// API socket or the directory that holds it.
func runtimeSocket(p string) bool {
	switch path.Base(p) {
	case "docker.sock", "containerd.sock", "podman.sock":
		return true
	}
	return p == "/run" || p == "/var/run" || p == "/run/docker" || p == "/var/run/docker"
}

// follow resolves the symlinks of an absolute path in the container
// without the denied-area checks; missing components stay as they are.
func (f *containerFS) follow(ctx context.Context, p string) (string, error) {
	cur := "/"
	for _, comp := range strings.Split(strings.TrimPrefix(path.Clean(p), "/"), "/") {
		if comp == "" {
			continue
		}
		next := path.Join(cur, comp)
		st, err := f.cli.ContainerStatPath(ctx, f.id, next)
		if err == nil && st.Mode&fs.ModeSymlink != 0 {
			t, err := linkTarget(st)
			if err != nil {
				return "", err
			}
			next = t
		}
		cur = next
	}
	return cur, nil
}

// hostPath maps a resolved container path to the host path behind the
// mount it lies in (the longest matching destination).
func (f *containerFS) hostPath(p string) (string, bool) {
	best := -1
	for i, m := range f.mounts {
		if m.Source == "" || !pathWithin(p, m.Destination) {
			continue
		}
		if best < 0 || len(m.Destination) > len(f.mounts[best].Destination) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	m := f.mounts[best]
	return path.Join(m.Source, strings.TrimPrefix(p, m.Destination)), true
}

func pathWithin(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// denied reports whether p, inside the container or on the host behind a
// mount such as node-exporter's /proc:/host/proc, is in a denied area.
func (f *containerFS) denied(p string) bool {
	if deniedContainerPath(p) {
		return true
	}
	hp, ok := f.hostPath(p)
	return ok && deniedContainerPath(hp)
}

// deniedBelow reports whether listing dir would tar a mount of a denied
// host area (or of a host directory containing one, such as /:/rootfs).
func (f *containerFS) deniedBelow(dir string) bool {
	for _, m := range f.mounts {
		if m.Source == "" || !pathWithin(m.Destination, dir) {
			continue
		}
		if deniedContainerPath(m.Source) {
			return true
		}
		for _, d := range deniedContainerRoots {
			if pathWithin(d, m.Source) {
				return true
			}
		}
	}
	return false
}

// resolve resolves every component of an absolute, clean path inside the
// container, following symlinks. The daemon evaluates a link in the scope
// of the container's root filesystem and reports the result as
// LinkTarget, so the walk never leaves that root; each step is also
// checked against the denied areas, so a link into /proc is refused the
// same as the path itself.
func (f *containerFS) resolve(ctx context.Context, clean string) (string, container.PathStat, error) {
	cur := "/"
	st, err := f.cli.ContainerStatPath(ctx, f.id, cur)
	if err != nil {
		return "", st, statErr(err)
	}
	for _, comp := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if comp == "" {
			continue
		}
		next := path.Join(cur, comp)
		if f.denied(next) {
			return "", st, ErrContainerPathDenied
		}
		st, err = f.cli.ContainerStatPath(ctx, f.id, next)
		if err != nil {
			return "", st, statErr(err)
		}
		if st.Mode&fs.ModeSymlink != 0 {
			target, err := linkTarget(st)
			if err != nil {
				return "", st, err
			}
			if f.denied(target) {
				return "", st, ErrContainerPathDenied
			}
			next = target
			st, err = f.cli.ContainerStatPath(ctx, f.id, next)
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

// resolveParent resolves the directory of clean and stats the last
// element itself without following it. exists is false when the last
// element doesn't exist yet.
func (f *containerFS) resolveParent(ctx context.Context, clean string) (dir, name string, st container.PathStat, exists bool, err error) {
	if clean == "/" {
		return "", "", st, false, ErrContainerNotFile
	}
	dir, dst, err := f.resolve(ctx, path.Dir(clean))
	if err != nil {
		return "", "", st, false, err
	}
	if !dst.Mode.IsDir() {
		return "", "", st, false, ErrContainerNotDir
	}
	name = path.Base(clean)
	full := path.Join(dir, name)
	if f.denied(full) {
		return "", "", st, false, ErrContainerPathDenied
	}
	st, err = f.cli.ContainerStatPath(ctx, f.id, full)
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

// owner returns the uid and gid of p from the first header of its tar.
func (f *containerFS) owner(ctx context.Context, p string) (int, int, error) {
	rdr, _, err := f.cli.CopyFromContainer(ctx, f.id, p)
	if err != nil {
		return 0, 0, statErr(err)
	}
	defer rdr.Close()
	hdr, err := tar.NewReader(rdr).Next()
	if err != nil {
		return 0, 0, err
	}
	return hdr.Uid, hdr.Gid, nil
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
	cfs, err := openContainerFS(ctx, cli, id)
	if err != nil {
		return nil, err
	}
	resolved, stat, err := cfs.resolve(ctx, clean)
	if err != nil {
		return nil, err
	}
	if !stat.Mode.IsDir() {
		return nil, ErrContainerNotDir
	}
	if cfs.deniedBelow(resolved) {
		return nil, ErrContainerMountBelow
	}

	rc, _, err := cli.CopyFromContainer(ctx, id, resolved)
	if err != nil {
		return nil, statErr(err)
	}
	defer rc.Close()
	// The daemon tars the whole subtree to list one level; stop reading
	// a huge one instead of letting a listing stream a whole volume.
	rdr := &io.LimitedReader{R: rc, N: maxBrowseTarBytes}

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
		if rdr.N <= 0 {
			return nil, ErrContainerDirTooLarge
		}
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
	cfs, err := openContainerFS(ctx, cli, id)
	if err != nil {
		return nil, nil, err
	}
	resolved, stat, err := cfs.resolve(ctx, clean)
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
	return &ContainerFileStat{Name: path.Base(path.Clean(p)), Size: hdr.Size}, nil
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
	if mode < 0 || mode > 0o777 {
		return ErrContainerModeInvalid
	}
	clean, err := sanitizeContainerPath(p)
	if err != nil {
		return err
	}
	cfs, err := openContainerFS(ctx, cli, id)
	if err != nil {
		return err
	}
	dir, name, st, exists, err := cfs.resolveParent(ctx, clean)
	if err != nil {
		return err
	}
	// The daemon extracts as root; the file keeps the owner it had, or
	// gets its directory's, instead of becoming root's.
	ownerOf := dir
	if exists {
		ownerOf = path.Join(dir, name)
	}
	uid, gid, err := cfs.owner(ctx, ownerOf)
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
		Uid:     uid,
		Gid:     gid,
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
	cfs, err := openContainerFS(ctx, cli, id)
	if err != nil {
		return err
	}
	dir, name, st, exists, err := cfs.resolveParent(ctx, clean)
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
		if code == 126 || code == 127 {
			return ErrContainerNoRm
		}
		return fmt.Errorf("rm exited with %d: %s", code, strings.TrimSpace(string(out)))
	}
	return nil
}

// ServeContainerFileRequest answers one req.container.files.* frame on
// the agent, so agent hosts run exactly the rules above.
func ServeContainerFileRequest(ctx context.Context, cli client.ContainerAPIClient, f agents.Frame) (any, error) {
	var req agents.ContainerFileReq
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, err
	}
	switch f.Type {
	case agents.FrameReqContainerFilesList:
		entries, err := BrowseContainerDir(ctx, cli, req.Container, req.Path)
		if entries == nil {
			entries = []VolumeEntry{}
		}
		return entries, err
	case agents.FrameReqContainerFilesRead:
		return ReadContainerFile(ctx, cli, req.Container, req.Path, req.MaxBytes)
	case agents.FrameReqContainerFilesStat:
		return StatContainerFile(ctx, cli, req.Container, req.Path)
	case agents.FrameReqContainerFilesWrite:
		return struct{}{}, WriteContainerFile(ctx, cli, req.Container, req.Path, req.Data, req.Mode)
	case agents.FrameReqContainerFilesDelete:
		return struct{}{}, DeleteContainerFile(ctx, cli, req.Container, req.Path)
	}
	return nil, fmt.Errorf("unknown request type: %s", f.Type)
}
