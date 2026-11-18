//go:build unix

package main

import (
	"io"

	"github.com/dockmesh/dockmesh/internal/stacksdir"
)

// Stacks dir check results, from the shared rules.
const (
	sdOK   = stacksdir.StatusOK
	sdWarn = stacksdir.StatusWarn
	sdFail = stacksdir.StatusFail

	sdUnchanged    = stacksdir.Unchanged
	sdNotAbsolute  = stacksdir.NotAbsolute
	sdBadChars     = stacksdir.BadChars
	sdSystemDir    = stacksdir.SystemDir
	sdInsideDocker = stacksdir.InsideDocker
	sdDockerRoot   = stacksdir.DockerRoot
	sdAgentData    = stacksdir.OwnDataDir
	sdServerData   = stacksdir.OtherDataDir
	sdInStacks     = stacksdir.InStacks
	sdBindMount    = stacksdir.BindMount
	sdFUSE         = stacksdir.FUSE
	sdSymlink      = stacksdir.Symlink
	sdNotDir       = stacksdir.NotDir
	sdUnsafeParent = stacksdir.UnsafeParent
	sdNewNotHere   = stacksdir.NewNotHere
	sdRootAgentDir = stacksdir.RootAgentDir
	sdNetworkShare = stacksdir.NetworkShare
	sdNoUnixPerms  = stacksdir.NoUnixPerms
	sdNotEmpty     = stacksdir.NotEmpty
	sdForeignOwner = stacksdir.ForeignOwner
	sdTooSmall     = stacksdir.TooSmall
	sdLowSpace     = stacksdir.LowSpace
	sdUnreadable   = stacksdir.Unreadable
)

// defaultServerDataDir is refused whether or not a server runs here.
const defaultServerDataDir = "/var/lib/dockmesh"

// stacksDirRules are the facts checkStacksDir decides on; see
// stacksdir.Rules.
type stacksDirRules struct {
	dataDir        string
	current        string
	serverDataDirs []string
	stackDirs      []string
	dockerRoots    []string
	uid            int
	account        string
	parentOK       func(path string, uid, mode uint32) bool
	mountinfo      func() (io.ReadCloser, error)
	deny           []string
	parents        []string
	devProtected   func(dev uint64) bool
}

type stacksDirResult struct {
	stacksdir.Result
}

func (r stacksDirResult) ok() bool { return r.OK() }

// checkStacksDir checks p as a new stacks dir of the agent with the rules
// of root's stacks-dir-apply. It writes nothing.
func checkStacksDir(p string, r stacksDirRules) stacksDirResult {
	return stacksDirResult{stacksdir.Check(p, stacksdir.Rules{
		DataDir:       r.dataDir,
		Current:       r.current,
		OtherDataDirs: r.serverDataDirs,
		StackDirs:     r.stackDirs,
		DockerRoots:   r.dockerRoots,
		UID:           r.uid,
		Account:       r.account,
		ParentOK:      r.parentOK,
		Mountinfo:     r.mountinfo,
		Deny:          r.deny,
		Parents:       r.parents,
		DevProtected:  r.devProtected,
	})}
}
