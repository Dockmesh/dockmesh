//go:build unix

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/selinux"
)

// runSystemCmd is `dockmesh-agent system <subcommand>`: host changes that
// need root, run by the installer.
func runSystemCmd(args []string) {
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(os.Stderr, "usage: dockmesh-agent system install [--data-dir <dir>] [--stacks-dir <dir>] [--user dockmesh-agent|root] [--socket-gid <gid>]")
		os.Exit(2)
	}
	if err := systemInstall(args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "system install:", err)
		os.Exit(1)
	}
}

// installOptions are the flags of `system install`; empty values keep what
// the host has, or take the default on a new install.
type installOptions struct {
	DataDir   string
	StacksDir string
	User      string
	SocketGID string
}

func systemInstall(args []string) error {
	fs := flag.NewFlagSet("system install", flag.ExitOnError)
	var o installOptions
	fs.StringVar(&o.DataDir, "data-dir", "", "data dir of the agent, chosen at the first install only (default "+defaultDataDir+")")
	fs.StringVar(&o.StacksDir, "stacks-dir", "", "stacks dir of the agent (default <data-dir>/stacks)")
	fs.StringVar(&o.User, "user", "", "account the agent runs as: "+agentAccount+" (default) or root, which only agents installed as root keep")
	fs.StringVar(&o.SocketGID, "socket-gid", "", "numeric group of the Docker socket, when it does not belong to docker")
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if runtime.GOOS != "linux" {
		return errors.New("the agent is installed with systemd on Linux only")
	}
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	in := hostInstaller(self)
	res, err := in.run(o)
	if err != nil {
		return err
	}
	for _, w := range res.warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	for _, f := range res.changed {
		fmt.Println("wrote", f)
	}
	if len(res.changed) == 0 {
		fmt.Println("the agent's files are up to date")
	}
	return in.activate(res.unitsChanged)
}

// agentInstaller writes the agent's binary, config and units. The paths
// and host lookups are fields so tests run it in a temp dir.
type agentInstaller struct {
	self    string
	binary  string
	envFile string
	unitDir string
	// rootUID and rootGID own the binary; rootUID also owns the config
	// dir and agent.env, and both own the dirs of an agent running as root.
	rootUID, rootGID int
	lookupAccount    func(name string) (uid, gid int, err error)
	dockerGID        func() int
	socketGroups     func(socket string, dockerGID int) ([]string, string)
	// rootOnly reports a directory only root can change, up to /.
	rootOnly func(dir string) bool
	// legacy reports what is left of the legacy layout's copy step.
	legacy    func(dataDir string, unitExists bool) error
	systemctl func(args ...string) error
}

func hostInstaller(self string) *agentInstaller {
	return &agentInstaller{
		self:          self,
		binary:        pathBinary,
		envFile:       agentEnvFile,
		unitDir:       filepath.Dir(agentUnitFile),
		lookupAccount: lookupAccount,
		dockerGID: func() int {
			g, err := user.LookupGroup("docker")
			if err != nil {
				return -1
			}
			gid, err := strconv.Atoi(g.Gid)
			if err != nil {
				return -1
			}
			return gid
		},
		socketGroups: layout.SocketGroups,
		rootOnly:     layout.RootOnlyDir,
		legacy:       hostLegacyLeftovers,
		systemctl: func(args ...string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, "/bin/systemctl", args...).CombinedOutput() // #nosec G204 -- fixed unit names
			if err != nil {
				return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

// lookupAccount returns the uid of account name and the gid of the group
// of the same name, which the unit runs with and which may read
// agent.env. A primary group of another name is refused: its members
// would read the config too.
func lookupAccount(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("look up %s: %w (the installer creates the account)", name, err)
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("look up the group %s: %w (the installer creates it)", name, err)
	}
	if u.Gid != g.Gid {
		return 0, 0, fmt.Errorf("the primary group of %s is not the group %s", name, name)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// hostLegacyLeftovers asks systemd too when the agent unit exists, so an
// ExecStartPre from a drop-in counts.
func hostLegacyLeftovers(dataDir string, unitExists bool) error {
	if unitExists {
		return legacyCopyStepAt(dataDir, legacyRefreshScript, agentUnitFile, false)
	}
	return legacyFilesLeft(dataDir, legacyRefreshScript)
}

type installResult struct {
	changed      []string
	unitsChanged bool
	warnings     []string
}

// plainDir is the set of characters a directory may use in agent.env and
// in the units: no quoting is needed in either.
var plainDir = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

func checkPlainDir(what, p string) (string, error) {
	if !plainDir.MatchString(p) || filepath.Clean(p) != p || p == "/" {
		return "", fmt.Errorf("%s %q must be a clean absolute path of letters, digits and ._-/", what, p)
	}
	return p, nil
}

func (in *agentInstaller) run(o installOptions) (installResult, error) {
	var res installResult
	env, envLines, err := in.readEnv()
	if err != nil {
		return res, err
	}
	unitPath := filepath.Join(in.unitDir, agentUnitName)
	oldUnit, err := layout.ReadUnitFile(unitPath)
	if err != nil {
		return res, err
	}

	dataDir := resolveDataDir(env["DOCKMESH_DATA_DIR"], fileExists)
	if o.DataDir != "" {
		if env["DOCKMESH_DATA_DIR"] != "" && filepath.Clean(o.DataDir) != filepath.Clean(env["DOCKMESH_DATA_DIR"]) {
			return res, fmt.Errorf("the data dir is %s and holds the agent's identity; it is chosen at the first install only", env["DOCKMESH_DATA_DIR"])
		}
		dataDir = o.DataDir
	}
	if dataDir, err = checkPlainDir("data dir", dataDir); err != nil {
		return res, err
	}
	if err := layout.CheckWritablePath(dataDir, ""); err != nil {
		return res, err
	}
	if err := in.legacy(dataDir, oldUnit != nil); err != nil {
		return res, fmt.Errorf("this host runs the legacy agent layout (%w); system install does not convert it", err)
	}

	runUser, err := pickRunUser(o.User, oldUnit)
	if err != nil {
		return res, err
	}
	uid, gid := in.rootUID, in.rootGID
	if runUser != "root" {
		if uid, gid, err = in.lookupAccount(runUser); err != nil {
			return res, err
		}
	}

	stacksDir := env["DOCKMESH_STACKS_DIR"]
	if o.StacksDir != "" {
		stacksDir = o.StacksDir
	}
	if stacksDir != "" {
		if stacksDir, err = checkStacksDirFlag(stacksDir, dataDir); err != nil {
			return res, err
		}
		if err := checkStacksDirParent(stacksDir, dataDir, in.rootOnly); err != nil {
			return res, err
		}
	}

	// The socket gid is kept in agent.env, so the unit an update writes
	// without flags still has it.
	socketGID := env["DOCKMESH_SOCKET_GID"]
	if o.SocketGID != "" {
		socketGID = o.SocketGID
	}
	if socketGID != "" {
		if socketGID, err = checkSocketGID(socketGID); err != nil {
			return res, err
		}
	}
	var groups []string
	unitUser := ""
	if runUser != "root" {
		unitUser = runUser
		var warn string
		groups, warn = in.socketGroups(layout.DockerSocketPath(env), in.dockerGID())
		if warn != "" {
			res.warnings = append(res.warnings, warn)
		}
		if socketGID != "" {
			groups = append(groups, socketGID)
		}
	}
	unit := layout.AgentUnit{
		Binary:              in.binary,
		EnvFile:             in.envFile,
		User:                unitUser,
		DataDir:             dataDir,
		SupplementaryGroups: groups,
	}
	if unit.WritablePaths, err = writableStackDirs(dataDir, stacksDir, env["DOCKMESH_EARLIER_STACKS_DIRS"], in.rootOnly); err != nil {
		return res, err
	}
	// Render before anything is written: a refused value changes nothing.
	if _, err := layout.RenderAgentUnit(unit); err != nil {
		return res, err
	}
	if _, _, err := layout.RenderAgentUpdateUnits(unit.Binary, unit.DataDir); err != nil {
		return res, err
	}

	dirs := []struct {
		path string
		mode os.FileMode
	}{{dataDir, 0o700}, {stacksDir, 0o750}}
	for _, d := range dirs {
		if d.path != "" {
			if err := checkAgentDir(d.path, uid); err != nil {
				return res, err
			}
		}
	}

	changed, err := in.installBinary()
	if err != nil {
		return res, fmt.Errorf("install %s: %w", in.binary, err)
	}
	if changed {
		res.changed = append(res.changed, in.binary)
	}
	for _, d := range dirs {
		if d.path == "" {
			continue
		}
		created, err := ensureAgentDir(d.path, d.mode, uid, gid)
		if err != nil {
			return res, err
		}
		if created {
			res.changed = append(res.changed, d.path)
		}
	}
	if err := layout.PrepareRootDir(filepath.Dir(in.envFile), in.rootUID, gid); err != nil {
		return res, err
	}
	envData := renderAgentEnv(envLines, map[string]string{
		"DOCKMESH_DATA_DIR": dataDir, "DOCKMESH_STACKS_DIR": stacksDir, "DOCKMESH_SOCKET_GID": socketGID,
	})
	if changed, err = layout.WriteRootFile(in.envFile, envData, in.rootUID, gid, 0o640); err != nil {
		return res, err
	}
	if changed {
		res.changed = append(res.changed, in.envFile)
	}
	units, err := layout.WriteAgentUnits(unit, in.unitDir)
	if err != nil {
		return res, err
	}
	res.unitsChanged = len(units) > 0
	res.changed = append(res.changed, units...)
	return res, nil
}

// activate reloads systemd after a unit change, enables the agent and its
// updater, and (re)starts the updater's path unit so it watches from now
// on. The agent itself is started by the installer.
func (in *agentInstaller) activate(unitsChanged bool) error {
	if unitsChanged {
		if err := in.systemctl("daemon-reload"); err != nil {
			return err
		}
	}
	path := filepath.Base(managedPathUnit)
	if err := in.systemctl("enable", agentUnitName, path, filepath.Base(managedServiceUnit)); err != nil {
		return err
	}
	verb := "start"
	if unitsChanged {
		verb = "restart"
	}
	return in.systemctl(verb, path)
}

// readEnv returns the agent's config and its lines, both empty when there
// is none yet. Like the unit writer it refuses a config root does not own.
func (in *agentInstaller) readEnv() (map[string]string, []string, error) {
	if _, err := os.Lstat(in.envFile); errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil, nil
	}
	env, err := layout.ReadEnvFile(in.envFile)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(in.envFile) // #nosec G304 -- the config ReadEnvFile just accepted
	if err != nil {
		return nil, nil, err
	}
	return env, strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// renderAgentEnv keeps every line of the config as it is, but sets the
// keys of set: the first line of each is replaced, later ones dropped, and
// a key not there yet is appended. An empty value removes the key.
func renderAgentEnv(lines []string, set map[string]string) []byte {
	done := map[string]bool{}
	var out []string
	for _, line := range lines {
		k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
		k = strings.TrimSpace(k)
		v, managed := set[k]
		if !ok || !managed {
			out = append(out, line)
			continue
		}
		if !done[k] && v != "" {
			out = append(out, k+"="+v)
		}
		done[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !done[k] && set[k] != "" {
			out = append(out, k+"="+set[k])
		}
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// pickRunUser keeps the account of an installed unit: an agent installed
// as root stays root, and switching is not done here.
func pickRunUser(flagUser string, oldUnit []byte) (string, error) {
	if flagUser != "" && flagUser != agentAccount && flagUser != "root" {
		return "", fmt.Errorf("--user must be %s or root", agentAccount)
	}
	if oldUnit == nil {
		if flagUser == "" {
			return agentAccount, nil
		}
		return flagUser, nil
	}
	have := unitUser(oldUnit)
	if have == "" {
		have = "root"
	}
	if have != agentAccount && have != "root" {
		return "", fmt.Errorf("the agent unit runs as %q; system install only writes units for %s or root", have, agentAccount)
	}
	if flagUser != "" && flagUser != have {
		return "", fmt.Errorf("the agent runs as %s; system install does not switch it to %s", have, flagUser)
	}
	return have, nil
}

// unitUser is the User= of a unit file, empty without one.
func unitUser(unit []byte) string {
	u := ""
	for _, line := range strings.Split(string(unit), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "User="); ok {
			u = strings.TrimSpace(v)
		}
	}
	return u
}

// checkStacksDirFlag refuses a stacks dir the unit writer would refuse, one
// below the data dir other than its default stacks/, and one that is the
// data dir or contains it.
func checkStacksDirFlag(p, dataDir string) (string, error) {
	p, err := checkPlainDir("stacks dir", p)
	if err != nil {
		return "", err
	}
	if p == dataDir {
		return "", fmt.Errorf("stacks dir %s is the data dir", p)
	}
	if strings.HasPrefix(p, dataDir+"/") && p != filepath.Join(dataDir, "stacks") {
		return "", fmt.Errorf("stacks dir %s lies in the data dir; only %s may", p, filepath.Join(dataDir, "stacks"))
	}
	if err := layout.CheckWritablePath(p, dataDir); err != nil {
		return "", err
	}
	return p, nil
}

// maxEarlierStacksDirs caps the earlier stacks dirs outside the data dir
// that still hold stacks, so the unit has at most four ReadWritePaths=
// entries: the data dir, the current stacks dir and these.
const maxEarlierStacksDirs = 2

// checkStacksDirParent refuses a stacks dir outside the data dir whose
// parent another account could change: systemd follows a symlink there
// at every start.
func checkStacksDirParent(p, dataDir string, rootOnly func(string) bool) error {
	if !strings.HasPrefix(p, dataDir+"/") && !rootOnly(filepath.Dir(p)) {
		return fmt.Errorf("%s and every directory above it must belong to root and be writable only by root", filepath.Dir(p))
	}
	return nil
}

// writableStackDirs returns the stacks dirs the unit makes writable: the
// current one and every earlier one that still holds a stack. Those in
// the data dir are left out, it is writable already.
func writableStackDirs(dataDir, current, earlier string, rootOnly func(string) bool) ([]string, error) {
	var out []string
	if current != "" && !strings.HasPrefix(current, dataDir+"/") {
		out = append(out, current)
	}
	dirs := resolveStackDirs(current, earlier, dataDir)
	for _, e := range dirs.earlier {
		p, err := checkStacksDirFlag(e, dataDir)
		if err == nil {
			err = checkStacksDirParent(p, dataDir, rootOnly)
		}
		if err != nil {
			return nil, fmt.Errorf("DOCKMESH_EARLIER_STACKS_DIRS: %w", err)
		}
		// The agent can make its own dir unreadable; that must not stop a
		// root update, so such a dir counts as empty.
		n, err := countStacks(p)
		if err != nil {
			slog.Warn("earlier stacks dir left out", "dir", p, "err", err)
			continue
		}
		if n == 0 || strings.HasPrefix(p, dataDir+"/") {
			continue
		}
		out = append(out, p)
	}
	if len(out) > 1+maxEarlierStacksDirs {
		return nil, fmt.Errorf("%d stacks dirs outside the data dir still hold stacks; the unit takes at most %d", len(out), 1+maxEarlierStacksDirs)
	}
	return out, nil
}

func checkSocketGID(s string) (string, error) {
	gid, err := strconv.ParseUint(s, 10, 32)
	if err != nil || gid == 0 {
		return "", fmt.Errorf("--socket-gid %q must be a numeric group other than 0", s)
	}
	if g, err := user.LookupGroupId(s); err == nil && layout.PrivilegedGroup(g.Name) {
		return "", fmt.Errorf("--socket-gid %s is the group %s, which is never given to the agent", s, g.Name)
	}
	return strconv.FormatUint(gid, 10), nil
}

// checkAgentDir accepts a dir that does not exist yet, or a real
// directory the agent's account owns.
func checkAgentDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
		return fmt.Errorf("%s exists and belongs to another account; move it away or choose a new directory", dir)
	}
	return nil
}

// ensureAgentDir creates dir for the agent's account unless it exists;
// checkAgentDir has accepted it.
func ensureAgentDir(dir string, mode os.FileMode, uid, gid int) (created bool, err error) {
	if _, err := os.Lstat(dir); err == nil {
		return false, checkAgentDir(dir, uid)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return false, fmt.Errorf("%s: %w (create its parent first)", dir, err)
	}
	defer parent.Close()
	name := filepath.Base(dir)
	if err := parent.Mkdir(name, mode); err != nil {
		return false, err
	}
	// Owner and mode go through the descriptor: in a parent the agent
	// account owns, the name could be swapped after the mkdir.
	f, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return true, err
	}
	defer f.Close()
	if err := f.Chown(uid, gid); err != nil {
		return true, err
	}
	return true, f.Chmod(mode)
}

// installBinary puts this binary at in.binary, owned by root and 0755, when
// it is not that file already. The copy is created in the target's
// directory, so the rename is atomic, and is then relabelled: systemd may
// only execute types like bin_t under SELinux.
func (in *agentInstaller) installBinary() (bool, error) {
	if in.self == in.binary {
		return false, nil
	}
	same, err := sameContent(in.self, in.binary)
	if err != nil {
		return false, err
	}
	if same && layout.HasOwnerMode(in.binary, in.rootUID, in.rootGID, 0o755) {
		return false, nil
	}
	src, err := os.Open(in.self)
	if err != nil {
		return false, err
	}
	defer src.Close()
	if err := layout.WriteRootFileFrom(in.binary, src, in.rootUID, in.rootGID, 0o755); err != nil {
		return false, err
	}
	selinux.Restore(slog.Default(), in.binary)
	return true, nil
}

func sameContent(a, b string) (bool, error) {
	ha, err := fileSHA256(a)
	if err != nil {
		return false, err
	}
	hb, err := fileSHA256(b)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}
