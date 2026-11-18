// Package stacksdir checks a new stacks directory with the rules root
// applies before it creates one for a service account or switches a
// service to it.
package stacksdir

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/dockmesh/dockmesh/internal/agents"
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

// AgentDataDir is the managed agent's data dir.
const AgentDataDir = "/var/lib/dockmesh-agent"

// ServerRules are the rules for a stacks dir of the Dockmesh server with
// data dir dataDir, owned by account uid; dockerRoots as DockerRoots
// returns them.
func ServerRules(dataDir string, uid int, account string, dockerRoots []string) Rules {
	def := filepath.Join(dataDir, "stacks")
	return Rules{
		DataDir:       dataDir,
		Current:       def,
		Service:       "Dockmesh",
		OtherDataDirs: []string{AgentDataDir},
		StackDirs:     []string{def},
		DockerRoots:   dockerRoots,
		UID:           uid,
		Account:       account,
		Mountinfo:     ProcMountinfo,
	}
}
