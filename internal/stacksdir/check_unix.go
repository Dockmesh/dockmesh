//go:build unix

// Package stacksdir checks a new stacks directory with the rules root
// applies before it creates one for a service account or switches a
// service to it.
package stacksdir

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// Statuses of a stacks dir check.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusFail = "fail"
)

// Stacks dir issues.
const (
	Unchanged    = "unchanged"
	NotAbsolute  = "not_absolute"
	BadChars     = "bad_characters"
	SystemDir    = "system_dir"
	InsideDocker = "inside_docker"
	DockerRoot   = "docker_root_unknown"
	OwnDataDir   = "agent_data_dir"
	OtherDataDir = "server_data_dir"
	InStacks     = "inside_stacks_dir"
	BindMount    = "bind_mount"
	FUSE         = "fuse"
	Symlink      = "symlink"
	NotDir       = "not_a_directory"
	UnsafeParent = "unsafe_parent"
	NewNotHere   = "new_dir_not_allowed"
	RootAgentDir = "root_agent_existing_dir"
	NetworkShare = "network_share"
	NoUnixPerms  = "no_unix_permissions"
	NotEmpty     = "not_empty"
	ForeignOwner = "foreign_owner"
	TooSmall     = "too_small"
	LowSpace     = "low_space"
	Unreadable   = "unreadable"
)

const (
	minStacksFree  = 200 << 20
	warnStacksFree = 2 << 30
)

// DenyRoots may never hold a stacks dir. Any top-level directory whose
// name starts with "lib" is refused as well. All of /var is refused, not
// only Docker's data: root services load drop-in directories from there
// (polkit's localauthority, systemd-sysext, k3s manifests), and a
// directory the agent may write in one of them would be a way to root
// that needs no Docker. The agent's default stacks dir is the exception.
var DenyRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/efi", "/proc", "/sys", "/dev", "/run", "/root", "/var", "/tmp", "/snap", "/nix"}

// DefaultParents are the directories below which root creates a new
// stacks dir, one level deep. Below any other tree root only accepts a
// directory an admin prepared: root services load code and config from
// trees no list of refused directories can name (a venv's site-packages
// under /opt, vendor plugin dirs), and a directory the agent may write in
// one of them would be a way to root.
var DefaultParents = []string{"/srv", "/opt", "/data", "/mnt", "/media"}

// Rules are the facts Check decides on. Root and the service fill them
// from the host; tests set them directly.
type Rules struct {
	// DataDir is the service's data dir; only DataDir/stacks may lie in it.
	DataDir string
	// Current is the stacks dir in use.
	Current string
	// Service names the service in messages; empty is "the agent".
	Service string
	// OtherDataDirs are data dirs of other Dockmesh services on this host.
	OtherDataDirs []string
	// StackDirs are the current and earlier stacks dirs; the service owns
	// them, so nothing may be created inside one.
	StackDirs []string
	// DockerRoots are the data roots of the Docker daemons on this host;
	// none when Docker could not be asked, which refuses every path.
	DockerRoots []string
	// UID must own a target that exists already; Account is its name,
	// used in the command that prepares one.
	UID     int
	Account string
	// ParentOK accepts a directory above the target; nil accepts only one
	// root owns and nobody else may write.
	ParentOK  func(path string, uid, mode uint32) bool
	Mountinfo func() (io.ReadCloser, error)
	// Deny and Parents replace DenyRoots and DefaultParents in tests,
	// which work below /tmp.
	Deny    []string
	Parents []string
	// DevProtected replaces the device check of a mount point in tests,
	// whose temp dirs share a device with /.
	DevProtected func(dev uint64) bool
}

func (r Rules) service() string {
	if r.Service == "" {
		return "the agent"
	}
	return r.Service
}

func (r Rules) allowedParents() []string {
	if r.Parents != nil {
		return r.Parents
	}
	return DefaultParents
}

// prepareCommand is what an admin runs to make p a directory the service
// may be switched to.
func (r Rules) prepareCommand(p string) string {
	return fmt.Sprintf("sudo install -d -o %s -g %s -m 0750 %s", r.Account, r.Account, p)
}

// CreatableName reports a name root may give a new stacks dir. The agent
// does not pick any name in a shared parent: some (/opt/cni, /srv/salt)
// are adopted by root tools installed later, which would then load code
// from a directory the agent owns. Names of Dockmesh's own server dirs
// (/srv/dockmesh, /opt/dockmesh) are left out as well.
func CreatableName(name string) bool {
	return name == "stacks" || stacksDirName.MatchString(name)
}

var stacksDirName = regexp.MustCompile(`^dockmesh-stacks[A-Za-z0-9_-]*$`)

// creatableParent reports whether root may create a stacks dir directly
// in dir: one of the allowed parents, or the mount point m of a local
// filesystem below one of them. A mount point whose device is that of /
// or of a refused directory is another view of a protected filesystem,
// such as / bound whole below /mnt.
func (r Rules) creatableParent(dir string, m setup.Mount, dev uint64, denied map[fileID]string) bool {
	for _, a := range r.allowedParents() {
		if dir == a {
			return true
		}
		if layout.Within(a, dir) && m.Mountpoint == dir && !setup.PseudoFS(m.FSType) && !r.protectedDev(dev, denied) {
			return true
		}
	}
	return false
}

func (r Rules) denied() []string {
	if r.Deny != nil {
		return r.Deny
	}
	return DenyRoots
}

func (r Rules) protectedDev(dev uint64, denied map[fileID]string) bool {
	if r.DevProtected != nil {
		return r.DevProtected(dev)
	}
	var st unix.Stat_t
	if unix.Stat("/", &st) != nil || uint64(st.Dev) == dev { //nolint:unconvert // the types differ per OS
		return true
	}
	for id := range denied {
		if id.dev == dev {
			return true
		}
	}
	return false
}

type Result struct {
	agents.StacksDirCheck
}

func (r Rules) parentAllowed(path string, uid, mode uint32) bool {
	if r.ParentOK != nil {
		return r.ParentOK(path, uid, mode)
	}
	return uid == 0 && mode&0o022 == 0
}

func (r Result) OK() bool { return r.Status != StatusFail }

func (r Result) withCommand(cmd string) Result {
	r.Command = cmd
	return r
}

// Check checks p as a new stacks dir with the rules of root's
// stacks-dir-apply. It writes nothing. Root creates p only as one new
// directory in an allowed parent; anywhere else p must exist already, be
// empty and belong to the agent. The refused trees below are a second
// line behind that. Every component is opened with O_NOFOLLOW from the
// one before, so a symlink anywhere is refused, and compared by device
// and inode with the refused directories, which catches another spelling
// of one. A path on a bind mount of a subdirectory is refused, since its
// real place is not known.
func Check(p string, r Rules) Result {
	res := Result{StacksDirCheck: agents.StacksDirCheck{Path: p}}
	fail := func(issue, format string, a ...any) Result {
		res.Status, res.Issue, res.Message = StatusFail, issue, fmt.Sprintf(format, a...)
		return res
	}
	if !filepath.IsAbs(p) {
		return fail(NotAbsolute, "Enter an absolute path, for example /srv/dockmesh/stacks.")
	}
	if len(p) > 1024 || !plainDir.MatchString(p) || filepath.Clean(p) != p {
		return fail(BadChars, "Use a clean path of only letters, digits and . _ - /.")
	}
	if p == r.Current {
		res.Status, res.Issue, res.Message = StatusOK, Unchanged, "This is the stacks directory already."
		return res
	}
	defaultDir := filepath.Join(r.DataDir, "stacks")
	switch {
	case p == "/" || p != defaultDir && DeniedByName(p, r.denied()):
		return fail(SystemDir, "%s is a system directory. Choose a directory of its own, for example /srv/dockmesh/stacks.", p)
	case len(r.DockerRoots) == 0:
		return fail(DockerRoot, "Docker's data directory is not known, so no path can be checked against it.")
	case p != defaultDir && (layout.Within(r.DataDir, p) || layout.Within(p, r.DataDir)):
		return fail(OwnDataDir, "%s overlaps the data directory of %s; only %s may lie in it.", p, r.service(), defaultDir)
	}
	for _, d := range r.DockerRoots {
		if layout.Within(d, p) || layout.Within(p, d) {
			return fail(InsideDocker, "Choose a directory outside Docker's data directory %s.", d)
		}
	}
	for _, d := range r.StackDirs {
		if p != d && layout.Within(d, p) {
			return fail(InStacks, "%s lies in the stacks directory %s. Choose a directory outside it.", p, d)
		}
	}
	for _, s := range r.OtherDataDirs {
		if layout.Within(s, p) || layout.Within(p, s) {
			return fail(OtherDataDir, "%s overlaps the data directory %s of another Dockmesh service.", p, s)
		}
	}

	denied := deniedIdentities(r, p == defaultDir)
	comps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(Unreadable, "Cannot open /: %v.", err)
	}
	defer func() { unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(Unreadable, "Cannot read /: %v.", err)
	}
	cur, existing := "/", 0
	var parentDev uint64
walk:
	for i, c := range comps {
		next := filepath.Join(cur, c)
		// O_DIRECTORY keeps a FIFO or device from blocking the open; what
		// is not a directory is told apart without following it.
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case errors.Is(err, unix.ENOENT):
			break walk
		case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
			var lst unix.Stat_t
			if unix.Fstatat(fd, c, &lst, unix.AT_SYMLINK_NOFOLLOW) == nil && lst.Mode&unix.S_IFMT == unix.S_IFLNK {
				return fail(Symlink, "%s is a symbolic link. Enter the real path.", next)
			}
			return fail(NotDir, "%s is not a directory.", next)
		case err != nil:
			return fail(Unreadable, "Cannot read %s: %v.", next, err)
		}
		unix.Close(fd)
		fd = nfd
		if i == len(comps)-1 {
			parentDev = uint64(st.Dev) //nolint:unconvert // the types differ per OS
		}
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(Unreadable, "Cannot read %s: %v.", next, err)
		}
		// A refused dir under its own name was decided by name above;
		// this catches other spellings of one.
		if why, bad := denied[fileID{uint64(st.Dev), uint64(st.Ino)}]; bad && why != next { //nolint:unconvert // the types differ per OS
			return fail(SystemDir, "%s is the same directory as %s.", next, why)
		}
		existing = i + 1
		last := i == len(comps)-1
		// Only root may be able to swap a directory above the target for
		// a symlink once root has checked it. The default dir lies in the
		// agent's own data dir, which the agent creates.
		if !last && p != defaultDir && !r.parentAllowed(next, st.Uid, uint32(st.Mode)) { //nolint:unconvert // the types differ per OS
			return fail(UnsafeParent, "%s and every directory above the stacks directory must belong to root and be writable only by root.", next)
		}
		cur = next
	}

	m, err := mountOf(r.Mountinfo, cur)
	if err != nil {
		return fail(Unreadable, "Cannot read the mount table: %v.", err)
	}
	res.FSType = m.FSType
	switch {
	case setup.NoUnixPermsFS(m.FSType):
		return fail(NoUnixPerms, "%s is on a filesystem without Unix permissions (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case setup.NetworkFS(m.FSType):
		return fail(NetworkShare, "%s is on a network share (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case m.FSType == "fuse" || strings.HasPrefix(m.FSType, "fuse."):
		return fail(FUSE, "%s is on a FUSE filesystem (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case m.Root != "/" && !m.Subvolume:
		return fail(BindMount, "%s is on a bind mount of %s. Enter a path on the mounted filesystem itself.", p, m.Root)
	}

	if existing < len(comps) {
		parentDev = uint64(st.Dev) //nolint:unconvert // the types differ per OS
	}
	parent, name := filepath.Dir(p), filepath.Base(p)
	creatable := func(pm setup.Mount) bool {
		return existing >= len(comps)-1 && CreatableName(name) && r.creatableParent(parent, pm, parentDev, denied)
	}
	prepare := r.prepareCommand(p)
	newRefused := func() Result {
		return fail(NewNotHere, "New directory: root creates a stacks directory only directly in %s or in a disk mounted below one of them, named stacks or starting with dockmesh-stacks. Prepare %s as an empty directory of %s: %s",
			strings.Join(r.allowedParents(), ", "), p, r.service(), prepare).withCommand(prepare)
	}
	switch {
	case existing == len(comps):
		// Every directory a root agent finds belongs to it, so ownership
		// says nothing about who prepared it; only what root could have
		// created itself is taken.
		if r.UID == 0 && p != defaultDir {
			pm, err := mountOf(r.Mountinfo, parent)
			if err != nil {
				return fail(Unreadable, "Cannot read the mount table: %v.", err)
			}
			if !creatable(pm) {
				return fail(RootAgentDir, "Existing directory: an agent running as root takes only a directory root may create, directly in %s or in a disk mounted below one of them, named stacks or starting with dockmesh-stacks.",
					strings.Join(r.allowedParents(), ", "))
			}
		}
		if int(st.Uid) != r.UID {
			return fail(ForeignOwner, "Existing directory: %s belongs to another account. Make it an empty directory of %s: %s", p, r.service(), prepare).withCommand(prepare)
		}
		empty, err := emptyDirAt(fd)
		if err != nil {
			return fail(Unreadable, "Cannot read %s: %v.", p, err)
		}
		if !empty {
			return fail(NotEmpty, "Existing directory: %s is not empty. Choose an empty directory, or prepare a new one: %s", p, prepare).withCommand(prepare)
		}
	case p != defaultDir && !creatable(m):
		return newRefused()
	default:
		res.WillCreate = true
	}

	var sfs unix.Statfs_t
	if err := unix.Fstatfs(fd, &sfs); err == nil {
		bsize := int64(sfs.Bsize)                  //nolint:unconvert,gosec // the type differs per OS; a block size is small
		res.TotalBytes = int64(sfs.Blocks) * bsize // #nosec G115 -- filesystem sizes fit int64
		res.FreeBytes = int64(sfs.Bavail) * bsize  // #nosec G115 -- filesystem sizes fit int64
		switch {
		case res.FreeBytes < minStacksFree:
			return fail(TooSmall, "Only %d MiB free on %s. Stacks need at least 200 MiB.", res.FreeBytes>>20, cur)
		case res.FreeBytes < warnStacksFree:
			res.Status, res.Issue = StatusWarn, LowSpace
			res.Message = fmt.Sprintf("Only %.1f GiB free. Images and bind data can fill this quickly.", float64(res.FreeBytes)/(1<<30))
			return res
		}
	}
	res.Status = StatusOK
	res.Message = "The directory can be used."
	return res
}

// DeniedByName refuses the deny roots and top-level lib* directories by
// name, ignoring case for filesystems that do.
func DeniedByName(p string, deny []string) bool {
	lower := strings.ToLower(p)
	for _, d := range deny {
		if layout.Within(d, lower) {
			return true
		}
	}
	top, _, _ := strings.Cut(strings.TrimPrefix(lower, "/"), "/")
	return strings.HasPrefix(top, "lib")
}

type fileID struct {
	dev uint64
	ino uint64
}

// deniedIdentities maps the device and inode of every refused directory
// that exists to its name. The agent's data dir is left out for its
// default stacks dir, which lies in it.
func deniedIdentities(r Rules, inDataDir bool) map[fileID]string {
	dirs := append(slices.Clone(r.DockerRoots), r.denied()...)
	dirs = append(dirs, r.OtherDataDirs...)
	if entries, err := os.ReadDir("/"); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(strings.ToLower(e.Name()), "lib") {
				dirs = append(dirs, "/"+e.Name())
			}
		}
	}
	if !inDataDir {
		dirs = append(dirs, r.DataDir)
	}
	out := map[fileID]string{}
	for _, d := range dirs {
		var st unix.Stat_t
		if d != "" && unix.Stat(d, &st) == nil {
			out[fileID{uint64(st.Dev), uint64(st.Ino)}] = d //nolint:unconvert // the types differ per OS
		}
	}
	return out
}

// mountOf is the mount that holds path.
func mountOf(mountinfo func() (io.ReadCloser, error), path string) (setup.Mount, error) {
	f, err := mountinfo()
	if err != nil {
		return setup.Mount{}, err
	}
	defer f.Close()
	return setup.MountAt(f, path)
}

func ProcMountinfo() (io.ReadCloser, error) { return os.Open("/proc/self/mountinfo") }

// emptyDirAt reports whether the directory behind a descriptor has no
// entries.
func emptyDirAt(dirFD int) (bool, error) {
	dfd, err := unix.Openat(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	d := os.NewFile(uintptr(dfd), "stacks dir") // #nosec G115 -- a descriptor just opened
	defer d.Close()
	names, err := d.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return len(names) == 0, err
}

// plainDir is the set of characters a stacks dir may use in a service's
// env file and unit.
var plainDir = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
