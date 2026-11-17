//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// Statuses of a stacks dir check.
const (
	sdOK   = "ok"
	sdWarn = "warn"
	sdFail = "fail"
)

// Stacks dir issues.
const (
	sdUnchanged    = "unchanged"
	sdNotAbsolute  = "not_absolute"
	sdBadChars     = "bad_characters"
	sdSystemDir    = "system_dir"
	sdInsideDocker = "inside_docker"
	sdDockerRoot   = "docker_root_unknown"
	sdAgentData    = "agent_data_dir"
	sdServerData   = "server_data_dir"
	sdInStacks     = "inside_stacks_dir"
	sdBindMount    = "bind_mount"
	sdFUSE         = "fuse"
	sdSymlink      = "symlink"
	sdNotDir       = "not_a_directory"
	sdUnsafeParent = "unsafe_parent"
	sdNewNotHere   = "new_dir_not_allowed"
	sdRootAgentDir = "root_agent_existing_dir"
	sdNetworkShare = "network_share"
	sdNoUnixPerms  = "no_unix_permissions"
	sdNotEmpty     = "not_empty"
	sdForeignOwner = "foreign_owner"
	sdTooSmall     = "too_small"
	sdLowSpace     = "low_space"
	sdUnreadable   = "unreadable"
)

const (
	minStacksFree  = 200 << 20
	warnStacksFree = 2 << 30
)

// denyRoots may never hold a stacks dir. Any top-level directory whose
// name starts with "lib" is refused as well. All of /var is refused, not
// only Docker's data: root services load drop-in directories from there
// (polkit's localauthority, systemd-sysext, k3s manifests), and a
// directory the agent may write in one of them would be a way to root
// that needs no Docker. The agent's default stacks dir is the exception.
var denyRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/efi", "/proc", "/sys", "/dev", "/run", "/root", "/var", "/tmp", "/snap", "/nix"}

// stacksParents are the directories below which root creates a new
// stacks dir, one level deep. Below any other tree root only accepts a
// directory an admin prepared: root services load code and config from
// trees no list of refused directories can name (a venv's site-packages
// under /opt, vendor plugin dirs), and a directory the agent may write in
// one of them would be a way to root.
var stacksParents = []string{"/srv", "/opt", "/data", "/mnt", "/media"}

// defaultServerDataDir is refused whether or not a server runs here.
const defaultServerDataDir = "/var/lib/dockmesh"

// stacksDirRules are the facts checkStacksDir decides on. Root and the
// agent fill them from the host; tests set them directly.
type stacksDirRules struct {
	dataDir string
	// current is the stacks dir in use.
	current string
	// serverDataDirs are data dirs of a Dockmesh server on this host.
	serverDataDirs []string
	// stackDirs are the current and earlier stacks dirs; the agent owns
	// them, so nothing may be created inside one.
	stackDirs []string
	// dockerRoots are the data roots of the Docker daemons on this host;
	// none when Docker could not be asked, which refuses every path.
	dockerRoots []string
	// uid must own a target that exists already; account is its name,
	// used in the command that prepares one.
	uid     int
	account string
	// parentOK accepts a directory above the target; nil accepts only one
	// root owns and nobody else may write.
	parentOK  func(path string, uid, mode uint32) bool
	mountinfo func() (io.ReadCloser, error)
	// deny and parents replace denyRoots and stacksParents in tests,
	// which work below /tmp.
	deny    []string
	parents []string
	// devProtected replaces the device check of a mount point in tests,
	// whose temp dirs share a device with /.
	devProtected func(dev uint64) bool
}

func (r stacksDirRules) allowedParents() []string {
	if r.parents != nil {
		return r.parents
	}
	return stacksParents
}

// prepareCommand is what an admin runs to make p a directory the agent
// may be switched to.
func (r stacksDirRules) prepareCommand(p string) string {
	return fmt.Sprintf("sudo install -d -o %s -g %s -m 0750 %s", r.account, r.account, p)
}

// creatableName reports a name root may give a new stacks dir. The agent
// does not pick any name in a shared parent: some (/opt/cni, /srv/salt)
// are adopted by root tools installed later, which would then load code
// from a directory the agent owns.
func creatableName(name string) bool {
	return name == "stacks" || strings.HasPrefix(name, "dockmesh")
}

// creatableParent reports whether root may create a stacks dir directly
// in dir: one of the allowed parents, or the mount point m of a local
// filesystem below one of them. A mount point whose device is that of /
// or of a refused directory is another view of a protected filesystem,
// such as / bound whole below /mnt.
func (r stacksDirRules) creatableParent(dir string, m setup.Mount, dev uint64, denied map[fileID]string) bool {
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

func (r stacksDirRules) denied() []string {
	if r.deny != nil {
		return r.deny
	}
	return denyRoots
}

func (r stacksDirRules) protectedDev(dev uint64, denied map[fileID]string) bool {
	if r.devProtected != nil {
		return r.devProtected(dev)
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

type stacksDirResult struct {
	agents.StacksDirCheck
}

func (r stacksDirRules) parentAllowed(path string, uid, mode uint32) bool {
	if r.parentOK != nil {
		return r.parentOK(path, uid, mode)
	}
	return uid == 0 && mode&0o022 == 0
}

func (r stacksDirResult) ok() bool { return r.Status != sdFail }

// checkStacksDir checks p as a new stacks dir with the rules of root's
// stacks-dir-apply. It writes nothing. Root creates p only as one new
// directory in an allowed parent; anywhere else p must exist already, be
// empty and belong to the agent. The refused trees below are a second
// line behind that. Every component is opened with O_NOFOLLOW from the
// one before, so a symlink anywhere is refused, and compared by device
// and inode with the refused directories, which catches another spelling
// of one. A path on a bind mount of a subdirectory is refused, since its
// real place is not known.
func checkStacksDir(p string, r stacksDirRules) stacksDirResult {
	res := stacksDirResult{StacksDirCheck: agents.StacksDirCheck{Path: p}}
	fail := func(issue, format string, a ...any) stacksDirResult {
		res.Status, res.Issue, res.Message = sdFail, issue, fmt.Sprintf(format, a...)
		return res
	}
	if !filepath.IsAbs(p) {
		return fail(sdNotAbsolute, "Enter an absolute path, for example /srv/dockmesh/stacks.")
	}
	if len(p) > 1024 || !plainDir.MatchString(p) || filepath.Clean(p) != p {
		return fail(sdBadChars, "Use a clean path of only letters, digits and . _ - /.")
	}
	if p == r.current {
		res.Status, res.Issue, res.Message = sdOK, sdUnchanged, "This is the stacks directory already."
		return res
	}
	defaultDir := filepath.Join(r.dataDir, "stacks")
	switch {
	case p == "/" || p != defaultDir && deniedByName(p, r.denied()):
		return fail(sdSystemDir, "%s is a system directory. Choose a directory of its own, for example /srv/dockmesh/stacks.", p)
	case len(r.dockerRoots) == 0:
		return fail(sdDockerRoot, "Docker's data directory is not known, so no path can be checked against it.")
	case p != defaultDir && (layout.Within(r.dataDir, p) || layout.Within(p, r.dataDir)):
		return fail(sdAgentData, "%s overlaps the agent's data directory; only %s may lie in it.", p, defaultDir)
	}
	for _, d := range r.dockerRoots {
		if layout.Within(d, p) || layout.Within(p, d) {
			return fail(sdInsideDocker, "Choose a directory outside Docker's data directory %s.", d)
		}
	}
	for _, d := range r.stackDirs {
		if p != d && layout.Within(d, p) {
			return fail(sdInStacks, "%s lies in the stacks directory %s. Choose a directory outside it.", p, d)
		}
	}
	for _, s := range r.serverDataDirs {
		if layout.Within(s, p) || layout.Within(p, s) {
			return fail(sdServerData, "%s overlaps the Dockmesh server's data directory %s.", p, s)
		}
	}

	denied := deniedIdentities(r, p == defaultDir)
	comps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(sdUnreadable, "Cannot open /: %v.", err)
	}
	defer func() { unix.Close(fd) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(sdUnreadable, "Cannot read /: %v.", err)
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
				return fail(sdSymlink, "%s is a symbolic link. Enter the real path.", next)
			}
			return fail(sdNotDir, "%s is not a directory.", next)
		case err != nil:
			return fail(sdUnreadable, "Cannot read %s: %v.", next, err)
		}
		unix.Close(fd)
		fd = nfd
		if i == len(comps)-1 {
			parentDev = uint64(st.Dev) //nolint:unconvert // the types differ per OS
		}
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(sdUnreadable, "Cannot read %s: %v.", next, err)
		}
		// A refused dir under its own name was decided by name above;
		// this catches other spellings of one.
		if why, bad := denied[fileID{uint64(st.Dev), uint64(st.Ino)}]; bad && why != next { //nolint:unconvert // the types differ per OS
			return fail(sdSystemDir, "%s is the same directory as %s.", next, why)
		}
		existing = i + 1
		last := i == len(comps)-1
		// Only root may be able to swap a directory above the target for
		// a symlink once root has checked it. The default dir lies in the
		// agent's own data dir, which the agent creates.
		if !last && p != defaultDir && !r.parentAllowed(next, st.Uid, uint32(st.Mode)) { //nolint:unconvert // the types differ per OS
			return fail(sdUnsafeParent, "%s and every directory above the stacks directory must belong to root and be writable only by root.", next)
		}
		cur = next
	}

	m, err := mountOf(r.mountinfo, cur)
	if err != nil {
		return fail(sdUnreadable, "Cannot read the mount table: %v.", err)
	}
	res.FSType = m.FSType
	switch {
	case setup.NoUnixPermsFS(m.FSType):
		return fail(sdNoUnixPerms, "%s is on a filesystem without Unix permissions (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case setup.NetworkFS(m.FSType):
		return fail(sdNetworkShare, "%s is on a network share (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case m.FSType == "fuse" || strings.HasPrefix(m.FSType, "fuse."):
		return fail(sdFUSE, "%s is on a FUSE filesystem (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, m.FSType)
	case m.Root != "/" && !m.Subvolume:
		return fail(sdBindMount, "%s is on a bind mount of %s. Enter a path on the mounted filesystem itself.", p, m.Root)
	}

	if existing < len(comps) {
		parentDev = uint64(st.Dev) //nolint:unconvert // the types differ per OS
	}
	parent, name := filepath.Dir(p), filepath.Base(p)
	creatable := func(pm setup.Mount) bool {
		return existing >= len(comps)-1 && creatableName(name) && r.creatableParent(parent, pm, parentDev, denied)
	}
	newRefused := func() stacksDirResult {
		return fail(sdNewNotHere, "New directory: root creates a stacks directory only directly in %s or in a disk mounted below one of them, named stacks or dockmesh…. Prepare %s as an empty directory of the agent: %s",
			strings.Join(r.allowedParents(), ", "), p, r.prepareCommand(p))
	}
	switch {
	case existing == len(comps):
		// Every directory a root agent finds belongs to it, so ownership
		// says nothing about who prepared it; only what root could have
		// created itself is taken.
		if r.uid == 0 && p != defaultDir {
			pm, err := mountOf(r.mountinfo, parent)
			if err != nil {
				return fail(sdUnreadable, "Cannot read the mount table: %v.", err)
			}
			if !creatable(pm) {
				return fail(sdRootAgentDir, "Existing directory: an agent running as root takes only a directory root may create, directly in %s or in a disk mounted below one of them, named stacks or dockmesh….",
					strings.Join(r.allowedParents(), ", "))
			}
		}
		if int(st.Uid) != r.uid {
			return fail(sdForeignOwner, "Existing directory: %s belongs to another account. Make it an empty directory of the agent: %s", p, r.prepareCommand(p))
		}
		empty, err := emptyDirAt(fd)
		if err != nil {
			return fail(sdUnreadable, "Cannot read %s: %v.", p, err)
		}
		if !empty {
			return fail(sdNotEmpty, "Existing directory: %s is not empty. Choose an empty directory, or prepare a new one: %s", p, r.prepareCommand(p))
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
			return fail(sdTooSmall, "Only %d MiB free on %s. Stacks need at least 200 MiB.", res.FreeBytes>>20, cur)
		case res.FreeBytes < warnStacksFree:
			res.Status, res.Issue = sdWarn, sdLowSpace
			res.Message = fmt.Sprintf("Only %.1f GiB free. Images and bind data can fill this quickly.", float64(res.FreeBytes)/(1<<30))
			return res
		}
	}
	res.Status = sdOK
	res.Message = "The directory can be used."
	return res
}

// deniedByName refuses the deny roots and top-level lib* directories by
// name, ignoring case for filesystems that do.
func deniedByName(p string, deny []string) bool {
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
func deniedIdentities(r stacksDirRules, inDataDir bool) map[fileID]string {
	dirs := append(slices.Clone(r.dockerRoots), r.denied()...)
	dirs = append(dirs, r.serverDataDirs...)
	if entries, err := os.ReadDir("/"); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(strings.ToLower(e.Name()), "lib") {
				dirs = append(dirs, "/"+e.Name())
			}
		}
	}
	if !inDataDir {
		dirs = append(dirs, r.dataDir)
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

func procMountinfo() (io.ReadCloser, error) { return os.Open("/proc/self/mountinfo") }

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
