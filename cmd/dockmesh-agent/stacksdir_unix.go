//go:build unix

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// Stacks dir issues, in the order checkStacksDir tests them.
const (
	sdUnchanged    = "unchanged"
	sdNotAbsolute  = "not_absolute"
	sdBadChars     = "bad_characters"
	sdSystemDir    = "system_dir"
	sdInsideDocker = "inside_docker"
	sdDockerRoot   = "docker_root_unknown"
	sdAgentData    = "agent_data_dir"
	sdServerData   = "server_data_dir"
	sdSymlink      = "symlink"
	sdNotDir       = "not_a_directory"
	sdUnsafeParent = "unsafe_parent"
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
// name starts with "lib" is refused as well.
var denyRoots = []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/proc", "/sys", "/dev", "/run", "/root", "/var/lib/docker"}

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
	// dockerRoot is Docker's data root; empty when Docker could not be
	// asked, which refuses every path.
	dockerRoot string
	// uid must own a target that exists already.
	uid int
	// parentOK accepts a directory above the target; nil accepts only one
	// root owns and nobody else may write.
	parentOK func(path string, uid, mode uint32) bool
	// mountinfo returns /proc/self/mountinfo.
	mountinfo func() (io.ReadCloser, error)
}

// stacksDirResult is a check and, for root, the walk it decided on.
type stacksDirResult struct {
	agents.StacksDirCheck
	// existing is the number of leading components of the path that exist.
	existing int
}

func (r stacksDirRules) parentAllowed(path string, uid, mode uint32) bool {
	if r.parentOK != nil {
		return r.parentOK(path, uid, mode)
	}
	return uid == 0 && mode&0o022 == 0
}

func (r stacksDirResult) ok() bool { return r.Status != "fail" }

// checkStacksDir checks p as a new stacks dir with the rules of root's
// stacks-dir-apply. It writes nothing. Every component is opened with
// O_NOFOLLOW from the one before, so a symlink anywhere is refused, and
// compared by device and inode with the refused directories, so a bind
// mount or another spelling of one is refused too.
func checkStacksDir(p string, r stacksDirRules) stacksDirResult {
	res := stacksDirResult{StacksDirCheck: agents.StacksDirCheck{Path: p}}
	fail := func(issue, format string, a ...any) stacksDirResult {
		res.Status, res.Issue, res.Message = "fail", issue, fmt.Sprintf(format, a...)
		return res
	}
	if !filepath.IsAbs(p) {
		return fail(sdNotAbsolute, "Enter an absolute path, for example /srv/dockmesh/stacks.")
	}
	if len(p) > 1024 || !plainDir.MatchString(p) || filepath.Clean(p) != p {
		return fail(sdBadChars, "Use a clean path of only letters, digits and . _ - /.")
	}
	if p == r.current {
		res.Status, res.Issue, res.Message = "ok", sdUnchanged, "This is the stacks directory already."
		return res
	}
	defaultDir := filepath.Join(r.dataDir, "stacks")
	switch {
	case p == "/" || deniedByName(p):
		return fail(sdSystemDir, "%s is a system directory. Choose a directory of its own, for example /srv/dockmesh/stacks.", p)
	case r.dockerRoot == "":
		return fail(sdDockerRoot, "Docker's data directory is not known, so no path can be checked against it.")
	case within(r.dockerRoot, p) || within(p, r.dockerRoot):
		return fail(sdInsideDocker, "Choose a directory outside Docker's data directory %s.", r.dockerRoot)
	case p != defaultDir && (within(r.dataDir, p) || within(p, r.dataDir)):
		return fail(sdAgentData, "%s overlaps the agent's data directory; only %s may lie in it.", p, defaultDir)
	}
	for _, s := range r.serverDataDirs {
		if within(s, p) || within(p, s) {
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
	cur := "/"
	for i, c := range comps {
		next := filepath.Join(cur, c)
		// O_DIRECTORY keeps a FIFO or device from blocking the open; what
		// is not a directory is told apart without following it.
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		switch {
		case errors.Is(err, unix.ENOENT):
		case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
			var lst unix.Stat_t
			if unix.Fstatat(fd, c, &lst, unix.AT_SYMLINK_NOFOLLOW) == nil && lst.Mode&unix.S_IFMT == unix.S_IFLNK {
				return fail(sdSymlink, "%s is a symbolic link. Enter the real path.", next)
			}
			return fail(sdNotDir, "%s is not a directory.", next)
		case err != nil:
			return fail(sdUnreadable, "Cannot read %s: %v.", next, err)
		}
		if errors.Is(err, unix.ENOENT) {
			break
		}
		unix.Close(fd)
		fd = nfd
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(sdUnreadable, "Cannot read %s: %v.", next, err)
		}
		if why, bad := denied[fileID{uint64(st.Dev), uint64(st.Ino)}]; bad { //nolint:unconvert // the types differ per OS
			return fail(sdSystemDir, "%s is the same directory as %s.", next, why)
		}
		res.existing = i + 1
		last := i == len(comps)-1
		// Root creates what is missing below the last existing directory;
		// only root may be able to replace any of them. The default dir
		// lies in the agent's own data dir, which the agent creates.
		if !last && p != defaultDir && !r.parentAllowed(next, st.Uid, uint32(st.Mode)) { //nolint:unconvert // the types differ per OS
			return fail(sdUnsafeParent, "%s and every directory above the stacks directory must belong to root and be writable only by root.", next)
		}
		cur = next
	}

	probe := cur
	fsType, err := mountFSType(r.mountinfo, probe)
	if err != nil {
		return fail(sdUnreadable, "Cannot read the mount table: %v.", err)
	}
	res.FSType = fsType
	switch {
	case setup.NetworkFS(fsType):
		return fail(sdNetworkShare, "%s is on a network share (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, fsType)
	case setup.NoUnixPermsFS(fsType):
		return fail(sdNoUnixPerms, "%s is on a filesystem without Unix permissions (%s). Choose a local filesystem such as ext4, xfs or btrfs.", p, fsType)
	}

	if res.existing == len(comps) {
		if int(st.Uid) != r.uid {
			return fail(sdForeignOwner, "%s exists and belongs to another account. Choose a new directory; it is created for the agent.", p)
		}
		empty, err := emptyDirAt(fd)
		if err != nil {
			return fail(sdUnreadable, "Cannot read %s: %v.", p, err)
		}
		if !empty {
			return fail(sdNotEmpty, "%s is not empty. Choose an empty or new directory.", p)
		}
	} else {
		res.WillCreate = true
	}

	var sfs unix.Statfs_t
	if err := unix.Fstatfs(fd, &sfs); err == nil {
		bsize := int64(sfs.Bsize)                  //nolint:unconvert,gosec // the type differs per OS; a block size is small
		res.TotalBytes = int64(sfs.Blocks) * bsize // #nosec G115 -- filesystem sizes fit int64
		res.FreeBytes = int64(sfs.Bavail) * bsize  // #nosec G115 -- filesystem sizes fit int64
		switch {
		case res.FreeBytes < minStacksFree:
			return fail(sdTooSmall, "Only %d MiB free on %s. Stacks need at least 200 MiB.", res.FreeBytes>>20, probe)
		case res.FreeBytes < warnStacksFree:
			res.Status, res.Issue = "warn", sdLowSpace
			res.Message = fmt.Sprintf("Only %.1f GiB free. Images and bind data can fill this quickly.", float64(res.FreeBytes)/(1<<30))
			return res
		}
	}
	res.Status = "ok"
	res.Message = "The directory can be used."
	return res
}

// deniedByName refuses the deny roots and top-level lib* directories by
// name, ignoring case for filesystems that do.
func deniedByName(p string) bool {
	lower := strings.ToLower(p)
	for _, d := range denyRoots {
		if within(d, lower) {
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
	dirs := append([]string{r.dockerRoot}, denyRoots...)
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

// mountFSType is the filesystem type of the mount that holds path.
func mountFSType(mountinfo func() (io.ReadCloser, error), path string) (string, error) {
	f, err := mountinfo()
	if err != nil {
		return "", err
	}
	defer f.Close()
	best, fsType := "", ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, fl := range fields {
			if fl == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(fields) {
			continue
		}
		mp := setup.UnescapeMount(fields[4])
		if within(mp, path) && len(mp) >= len(best) {
			best, fsType = mp, fields[sep+1]
		}
	}
	return fsType, sc.Err()
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

// within reports whether p is root or lies below it; both are clean.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}
