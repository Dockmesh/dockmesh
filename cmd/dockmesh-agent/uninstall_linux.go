package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/client"
	"golang.org/x/term"
)

func watchUninstallSignal(ctx context.Context) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				signal.Stop(ch)
				return
			case <-ch:
				requestUninstallReport()
			}
		}
	}()
}

// runUninstallCmd is `dockmesh-agent uninstall [--purge] [--yes] [--dry-run]`.
func runUninstallCmd(args []string) {
	fset := flag.NewFlagSet("uninstall", flag.ExitOnError)
	var o uninstallOptions
	fset.BoolVar(&o.purge, "purge", false, "also remove the data dir, the stacks dirs and the "+agentAccount+" user")
	fset.BoolVar(&o.yes, "yes", false, "do not ask")
	fset.BoolVar(&o.dryRun, "dry-run", false, "list what would be removed and kept, change nothing")
	_ = fset.Parse(args)
	if fset.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fset.Arg(0))
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "dockmesh-agent uninstall must run as root (sudo dockmesh-agent uninstall).")
		os.Exit(1)
	}
	u := hostUninstaller()
	if err := u.run(o, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type uninstallOptions struct {
	purge, yes, dryRun bool
}

// uninstaller removes what the installer and the agent put on a host.
// Every path is below root, which is "/" except in tests.
type uninstaller struct {
	root string
	env  map[string]string
	// interactive reports a terminal on stdin; color one on stdout.
	interactive, color bool
	hostname           string
	systemctl          func(args ...string) error
	unitActive         func() bool
	// notify asks the running agent to send the uninstalled frame.
	notify      func() error
	notifyWait  time.Duration
	runningIn   func(stackNames []string) int
	lookupUser  func(name string) (uid int, ok bool)
	deleteUser  func(name string) error
	lockState   func() (func(), error)
	systemUIDTo int
}

func hostUninstaller() *uninstaller {
	env, _ := layout.ReadEnvFile(agentEnvFile)
	if env == nil {
		env = map[string]string{}
	}
	hn, _ := os.Hostname()
	in := hostInstaller("")
	return &uninstaller{
		root:        "/",
		env:         env,
		interactive: term.IsTerminal(int(os.Stdin.Fd())),
		color:       term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && os.Getenv("TERM") != "",
		hostname:    hn,
		systemctl:   in.systemctl,
		unitActive: func() bool {
			return exec.Command("/bin/systemctl", "is-active", "--quiet", agentUnitName).Run() == nil // #nosec G204 -- fixed unit name
		},
		notify: func() error {
			return in.systemctl("kill", "--kill-whom=main", "--signal=SIGUSR1", agentUnitName)
		},
		notifyWait: 15 * time.Second,
		runningIn:  runningStackContainers,
		lookupUser: func(name string) (int, bool) {
			u, err := user.Lookup(name)
			if err != nil {
				return 0, false
			}
			uid, err := strconv.Atoi(u.Uid)
			return uid, err == nil
		},
		deleteUser: func(name string) error {
			out, err := exec.Command("userdel", name).CombinedOutput() // #nosec G204 -- fixed account name
			if err != nil {
				return fmt.Errorf("userdel %s: %w: %s", name, err, strings.TrimSpace(string(out)))
			}
			if _, gerr := user.LookupGroup(name); gerr == nil {
				if out, err := exec.Command("groupdel", name).CombinedOutput(); err != nil { // #nosec G204 -- fixed group name
					return fmt.Errorf("groupdel %s: %w: %s", name, err, strings.TrimSpace(string(out)))
				}
			}
			return nil
		},
		lockState:   func() (func(), error) { return lockStacksDir(managedStateDir) },
		systemUIDTo: 1000,
	}
}

// runningStackContainers counts the running containers of the compose
// projects named like the stacks; -1 when Docker can't be asked.
func runningStackContainers(names []string) int {
	if len(names) == 0 {
		return 0
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return -1
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return -1
	}
	n := 0
	for _, c := range res.Items {
		if slices.Contains(names, c.Labels["com.docker.compose.project"]) {
			n++
		}
	}
	return n
}

// Files the agent and the installer write in the data dir. staging/ and
// update/ hold only what the agent staged.
var (
	agentDataFiles = []string{
		"agent.crt", "agent.key", "ca.crt", "agent.url", enrollTokenFile,
		"agent.crt.next", "agent.key.next", "enroll-ca.pem", agentNameFile, uninstalledFile,
	}
	agentDataDirs = []string{"staging", "update"}
	// agentStackFiles are the files the agent writes into a stack dir;
	// .env is the copy older agents kept.
	agentStackFiles = []string{"compose.yaml", ".dockmesh.meta.json", ".env"}
	agentEtcFiles   = []string{"agent.env", filepath.Base(legacyRefreshScript)}
)

// ownFile reports a file Dockmesh wrote, or the temp file of one of them.
func ownFile(name string, known []string) bool {
	for _, k := range known {
		if name == k || strings.HasPrefix(name, "."+k+".") {
			return true
		}
	}
	return false
}

type planLine struct{ verb, text string }

type uninstallPlan struct {
	name    string
	dataDir string
	legacy  bool
	lines   []planLine
	// steps run after the agent stopped, in order.
	steps []planStep
	// foreign are kept dirs with files Dockmesh did not write.
	foreign  []string
	running  int
	stackDir string
	purge    bool
}

type planStep struct {
	what string
	do   func() error
}

func (p *uninstallPlan) remove(text string, steps ...planStep) {
	p.lines = append(p.lines, planLine{"remove", text})
	p.steps = append(p.steps, steps...)
}

func (p *uninstallPlan) keep(text, reason string) {
	p.lines = append(p.lines, planLine{"keep", text + ": " + reason})
}

func (u *uninstaller) path(p string) string { return filepath.Join(u.root, p) }

func (u *uninstaller) exists(p string) bool {
	_, err := os.Lstat(u.path(p))
	return err == nil
}

// dataDirOf resolves the data dir the way the agent does; a value that is
// not a plain absolute path is refused.
func (u *uninstaller) dataDirOf() (string, error) {
	d := resolveDataDir(u.env["DOCKMESH_DATA_DIR"], func(p string) bool { return u.exists(p) })
	if _, err := checkPlainDir("data dir", d); err != nil {
		return "", err
	}
	return d, nil
}

func (u *uninstaller) plan(o uninstallOptions) (*uninstallPlan, error) {
	dataDir, err := u.dataDirOf()
	if err != nil {
		return nil, err
	}
	p := &uninstallPlan{dataDir: dataDir, purge: o.purge}
	p.name = u.hostname
	if b, err := readDataFile(u.path(dataDir), agentNameFile, maxAgentName+1); err == nil {
		if n := cleanAgentName(b); n != "" {
			p.name = n
		}
	}
	p.legacy = u.exists(legacyRefreshScript) || u.exists(filepath.Join(dataDir, "bin", "dockmesh-agent"))

	u.planUnits(p)
	u.planBinary(p)
	u.planEtc(p)
	shared := serverDataDirAt(u.path(dataDir), u.root, func(f string) bool {
		_, err := os.Lstat(f)
		return err == nil
	})
	dirs := u.stackDirs(dataDir)
	var names []string
	for _, d := range dirs {
		names = append(names, u.planStacks(p, d, dataDir, shared)...)
	}
	u.planData(p, dataDir, shared)
	u.planUser(p)
	if len(names) > 0 && u.runningIn != nil {
		p.running = u.runningIn(names)
		p.stackDir = dirs[0]
	}
	u.planState(p)
	return p, nil
}

func (u *uninstaller) planUnits(p *uninstallPlan) {
	var names []string
	var steps []planStep
	for _, f := range []string{agentUnitFile, managedPathUnit, managedServiceUnit} {
		if !u.exists(f) {
			continue
		}
		names = append(names, filepath.Base(f))
		steps = append(steps, u.removeFile(f))
	}
	if len(names) > 0 {
		p.remove(unitList(names), steps...)
	}
	for _, f := range []string{agentUnitFile, managedPathUnit, managedServiceUnit} {
		if u.exists(f + ".d") {
			p.keep(f+".d", "drop-ins are not written by Dockmesh; remove them yourself")
		}
	}
}

// unitList shortens dockmesh-agent-update.path, dockmesh-agent-update.service
// to dockmesh-agent-update.path/.service.
func unitList(names []string) string {
	out := []string{}
	for i := 0; i < len(names); i++ {
		n := names[i]
		if i+1 < len(names) && strings.TrimSuffix(n, path.Ext(n)) == strings.TrimSuffix(names[i+1], path.Ext(names[i+1])) {
			out = append(out, n+"/"+path.Ext(names[i+1]))
			i++
			continue
		}
		out = append(out, n)
	}
	return strings.Join(out, ", ")
}

func (u *uninstaller) planBinary(p *uninstallPlan) {
	var shown []string
	var steps []planStep
	if p.legacy {
		bin := filepath.Join(p.dataDir, "bin", "dockmesh-agent")
		if u.exists(bin) {
			shown = append(shown, bin)
			steps = append(steps, u.removeBelow(p.dataDir, "bin/dockmesh-agent"), u.removeEmptyBelow(p.dataDir, "bin"))
		}
	}
	if u.exists(pathBinary) {
		shown = append(shown, pathBinary)
		steps = append(steps, u.removeFile(pathBinary))
	}
	if u.exists(pathBinary + ".prev") {
		steps = append(steps, u.removeFile(pathBinary+".prev"))
	}
	if len(shown) > 0 {
		p.remove(strings.Join(shown, " and "), steps...)
	}
}

func (u *uninstaller) planEtc(p *uninstallPlan) {
	dir := filepath.Dir(agentEnvFile)
	entries, err := os.ReadDir(u.path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		p.keep(dir, err.Error())
		return
	}
	var steps []planStep
	var other []string
	for _, e := range entries {
		if e.Type().IsRegular() && ownFile(e.Name(), agentEtcFiles) {
			steps = append(steps, u.removeFile(filepath.Join(dir, e.Name())))
			continue
		}
		other = append(other, e.Name())
	}
	if len(other) > 0 {
		p.remove(dir+" (Dockmesh's files)", steps...)
		p.keep(dir, "contains files not written by Dockmesh (e.g. "+other[0]+")")
		p.foreign = append(p.foreign, dir)
		return
	}
	p.remove(dir, append(steps, u.removeEmpty(dir))...)
}

// stackDirs lists the configured stacks dirs that exist, the current one
// first.
func (u *uninstaller) stackDirs(dataDir string) []string {
	d := resolveStackDirs(u.env["DOCKMESH_STACKS_DIR"], u.env["DOCKMESH_EARLIER_STACKS_DIRS"], dataDir)
	all := d.all()
	if def := filepath.Join(dataDir, "stacks"); !slices.Contains(all, def) {
		all = append(all, def)
	}
	var out []string
	for _, s := range all {
		if fi, err := os.Lstat(u.path(s)); err == nil && (fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0) {
			out = append(out, s)
		}
	}
	return out
}

// planStacks plans one stacks dir and returns the names of its stacks.
func (u *uninstaller) planStacks(p *uninstallPlan, dir, dataDir string, shared bool) []string {
	inData := layout.Within(dataDir, dir)
	if _, err := checkPlainDir("stacks dir", dir); err != nil {
		p.keep(dir, err.Error())
		return nil
	}
	if real, err := filepath.EvalSymlinks(u.path(dir)); err != nil || real != u.path(dir) {
		p.keep(dir, "its path contains a symbolic link; it is never followed")
		p.foreign = append(p.foreign, dir)
		return nil
	}
	if !inData {
		if err := layout.CheckWritablePath(dir, dataDir); err != nil {
			p.keep(dir, err.Error())
			return nil
		}
	}
	scan, err := scanStacks(u.path(dir))
	if err != nil {
		p.keep(dir, err.Error())
		return nil
	}
	label := fmt.Sprintf("%s (%s)", dir, plural(len(scan.stacks), "stack"))
	switch {
	case shared && inData:
		p.keep(label, "shared with the Dockmesh server on this host")
		return scan.stacks
	case !p.purge:
		p.keep(label, "kept by default, use --purge to remove")
		return scan.stacks
	case scan.foreign != "":
		p.keep(dir, "contains files not written by Dockmesh (e.g. "+scan.foreign+")")
		p.foreign = append(p.foreign, dir)
		return scan.stacks
	}
	steps := []planStep{u.removeScanned(dir, scan)}
	if inData {
		// Shown with the data dir.
		p.steps = append(p.steps, steps...)
		return scan.stacks
	}
	if u.createdByDockmesh(dir) {
		p.remove(label, append(steps, u.removeEmpty(dir))...)
		return scan.stacks
	}
	p.remove(label+" contents", steps...)
	p.keep(dir, "the directory was not created by Dockmesh; it is empty now")
	return scan.stacks
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// stacksScan is what a stacks dir holds: its stack names, the entries
// Dockmesh may remove (deepest first) and the first foreign entry.
type stacksScan struct {
	stacks  []string
	entries []string
	foreign string
}

// scanStacks walks dir below an os.Root without following links. A stack
// dir may hold the agent's files and directories that hold nothing else,
// such as the empty bind sources Docker creates; anything else is foreign.
func scanStacks(dir string) (stacksScan, error) {
	var s stacksScan
	root, err := os.OpenRoot(dir)
	if err != nil {
		return s, err
	}
	defer root.Close()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		depth := strings.Count(p, "/") + 1
		switch {
		case d.IsDir() && depth == 1 && stacks.ValidateName(p) == nil:
			s.stacks = append(s.stacks, p)
		case d.IsDir() && depth > 1:
		case d.Type().IsRegular() && depth == 2 && slices.Contains(agentStackFiles, d.Name()):
		default:
			if s.foreign == "" {
				s.foreign = p
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		s.entries = append(s.entries, p)
		return nil
	})
	slices.Reverse(s.entries)
	return s, err
}

// removeScanned removes the scanned entries one by one, so a file a
// container writes meanwhile makes its directory stay instead of being
// deleted with it.
func (u *uninstaller) removeScanned(dir string, s stacksScan) planStep {
	return planStep{what: dir, do: func() error {
		root, err := os.OpenRoot(u.path(dir))
		if err != nil {
			return err
		}
		defer root.Close()
		var errs []error
		for _, e := range s.entries {
			if err := root.Remove(e); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: %w", filepath.Join(dir, e), err))
			}
		}
		return errors.Join(errs...)
	}}
}

func (u *uninstaller) planData(p *uninstallPlan, dataDir string, shared bool) {
	root := u.path(dataDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		p.keep(dataDir, err.Error())
		return
	}
	// Every known name, as the agent writes uninstalledFile after the plan.
	var steps []planStep
	for _, n := range agentDataFiles {
		steps = append(steps, u.removeBelow(dataDir, n))
	}
	var other []string
	for _, e := range entries {
		n := e.Name()
		switch {
		case e.Type().IsRegular() && slices.Contains(agentDataFiles, n):
		case e.Type().IsRegular() && ownFile(n, agentDataFiles):
			steps = append(steps, u.removeBelow(dataDir, n))
		case e.IsDir() && slices.Contains(agentDataDirs, n):
			steps = append(steps, u.removeAllBelow(dataDir, n))
		case n == "bin" && p.legacy && e.IsDir():
		case n == "stacks" && e.IsDir() && !slices.Contains(p.foreign, filepath.Join(dataDir, n)):
		default:
			other = append(other, n)
		}
	}
	if !p.purge {
		p.remove("certificate and key, staging/, update/ in "+dataDir, steps...)
		return
	}
	switch {
	case shared:
		p.remove("the agent's files in "+dataDir, steps...)
		p.keep(dataDir, "the Dockmesh server keeps its data here")
	case len(other) > 0:
		p.remove("the agent's files in "+dataDir, steps...)
		p.keep(dataDir, "contains files not written by Dockmesh (e.g. "+other[0]+")")
		p.foreign = append(p.foreign, dataDir)
	case !u.createdByDockmesh(dataDir):
		p.remove("the agent's files in "+dataDir, steps...)
		p.keep(dataDir, "the directory was not created by Dockmesh; it is empty now")
	default:
		what := dataDir + " (data)"
		if u.exists(filepath.Join(dataDir, "stacks")) {
			what = dataDir + " (data and stacks)"
		}
		p.remove(what, append(steps, u.removeEmptyBelow(dataDir, "stacks"), u.removeEmpty(dataDir))...)
	}
}

// planUser removes the agent's account on --purge unless kept files still
// belong to it. Only a system account of that name is removed.
func (u *uninstaller) planUser(p *uninstallPlan) {
	uid, ok := u.lookupUser(agentAccount)
	if !ok {
		return
	}
	label := "user " + agentAccount
	switch {
	case !p.purge:
		p.keep(label, "kept by default, use --purge to remove")
	case uid == 0 || uid >= u.systemUIDTo:
		p.keep(label, "not a system account, so not created by the installer")
	case len(p.foreign) > 0:
		p.keep(label, "owns the kept "+p.foreign[0])
	default:
		p.remove(label, planStep{what: label, do: func() error { return u.deleteUser(agentAccount) }})
	}
}

// planState removes root's update state; nothing else writes there.
func (u *uninstaller) planState(p *uninstallPlan) {
	if fi, err := os.Lstat(u.path(managedStateDir)); err == nil && fi.IsDir() {
		p.steps = append(p.steps, planStep{what: managedStateDir, do: func() error {
			return os.RemoveAll(u.path(managedStateDir))
		}})
	}
}

// createdByDockmesh reports a directory Dockmesh made: the default data
// dirs, or one root recorded when it created it.
func (u *uninstaller) createdByDockmesh(dir string) bool {
	if dir == defaultDataDir || dir == legacyDataDir {
		return true
	}
	return slices.Contains(readCreatedDirs(u.path(managedStateDir)), dir)
}

func (u *uninstaller) removeFile(p string) planStep {
	return planStep{what: p, do: func() error {
		if err := os.Remove(u.path(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}}
}

func (u *uninstaller) removeEmpty(dir string) planStep {
	return planStep{what: dir, do: func() error {
		err := syscall.Rmdir(u.path(dir))
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		return nil
	}}
}

// removeBelow, removeEmptyBelow and removeAllBelow work below an os.Root
// on dir, which the agent's account owns.
func (u *uninstaller) removeBelow(dir, name string) planStep {
	return u.belowRoot(dir, name, func(r *os.Root) error { return r.Remove(name) })
}

func (u *uninstaller) removeEmptyBelow(dir, name string) planStep {
	return u.belowRoot(dir, name, func(r *os.Root) error {
		fi, err := r.Lstat(name)
		if err != nil || !fi.IsDir() {
			return err
		}
		return r.Remove(name)
	})
}

func (u *uninstaller) removeAllBelow(dir, name string) planStep {
	return u.belowRoot(dir, name, func(r *os.Root) error { return r.RemoveAll(name) })
}

func (u *uninstaller) belowRoot(dir, name string, op func(*os.Root) error) planStep {
	return planStep{what: filepath.Join(dir, name), do: func() error {
		r, err := os.OpenRoot(u.path(dir))
		if err != nil {
			return err
		}
		defer r.Close()
		if err := op(r); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}}
}

func (u *uninstaller) run(o uninstallOptions, stdin io.Reader, out io.Writer) error {
	p, err := u.plan(o)
	if err != nil {
		return err
	}
	pr := printer{w: out, color: u.color}
	header := "Dockmesh agent uninstall · " + p.name
	if o.dryRun {
		header += " · dry run"
	}
	pr.line(header)
	pr.line("")
	for _, l := range p.lines {
		pr.line(fmt.Sprintf("  %-6s  %s", l.verb, l.text))
	}
	if p.running > 0 {
		pr.line("")
		pr.warn(fmt.Sprintf("%s of Dockmesh stacks %s running. They keep running.", plural(p.running, "container"), isAre(p.running)))
		pr.line(fmt.Sprintf("  Manage them with docker compose in %s/<stack>.", p.stackDir))
	}
	if o.dryRun {
		return nil
	}
	if !o.yes {
		if !u.interactive {
			return errors.New("no terminal to ask on; run again with --yes to uninstall without a prompt")
		}
		if !confirm(pr, bufio.NewReader(stdin), o.purge, p.name) {
			return errors.New("nothing was changed")
		}
	}
	unlock, err := u.lockState()
	if err != nil {
		return err
	}
	defer unlock()

	removed := u.tellServer(p.dataDir)
	units := []string{"disable", "--now"}
	for _, f := range []string{agentUnitFile, managedPathUnit, managedServiceUnit} {
		if u.exists(f) {
			units = append(units, filepath.Base(f))
		}
	}
	if len(units) > 2 {
		if err := u.systemctl(units...); err != nil {
			return fmt.Errorf("stop the agent: %w; nothing was removed", err)
		}
	}
	var failed []error
	for _, s := range p.steps {
		if err := s.do(); err != nil {
			failed = append(failed, err)
		}
	}
	if err := u.systemctl("daemon-reload"); err != nil {
		failed = append(failed, err)
	}
	pr.line("")
	for _, err := range failed {
		pr.warn("not removed: " + err.Error())
	}
	if len(failed) > 0 {
		return errors.New("the agent is stopped, but the files above are left; remove them yourself")
	}
	if removed {
		pr.ok(fmt.Sprintf("The agent is removed. Dockmesh shows %s as \"Removed from host\".", p.name))
	} else {
		pr.ok(fmt.Sprintf("The agent is removed. Delete %s in Dockmesh: Hosts › %s › Delete host.", p.name, p.name))
	}
	if len(p.foreign) > 0 {
		pr.line(fmt.Sprintf("    Kept: %s. Remove it yourself if you no longer need it.", strings.Join(p.foreign, ", ")))
	}
	return nil
}

// tellServer asks the running agent to report the uninstall and waits for
// the server's answer; false when the agent is not connected.
func (u *uninstaller) tellServer(dataDir string) bool {
	if u.unitActive == nil || !u.unitActive() {
		return false
	}
	dir := u.path(dataDir)
	_ = u.removeBelow(dataDir, uninstalledFile).do()
	if err := u.notify(); err != nil {
		return false
	}
	deadline := time.Now().Add(u.notifyWait)
	for time.Now().Before(deadline) {
		if _, err := readDataFile(dir, uninstalledFile, 64); err == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func confirm(pr printer, in *bufio.Reader, purge bool, name string) bool {
	pr.line("")
	if !purge {
		pr.prompt("Continue? [y/N] ")
		ans, _ := in.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		return ans == "y" || ans == "yes"
	}
	pr.prompt("This deletes the stack files. Containers and volumes stay. Type the host name to continue: ")
	ans, _ := in.ReadString('\n')
	return strings.TrimSpace(ans) == name
}

type printer struct {
	w     io.Writer
	color bool
}

func (p printer) line(s string)   { fmt.Fprintln(p.w, s) }
func (p printer) prompt(s string) { fmt.Fprint(p.w, s) }

func (p printer) paint(code, s string) string {
	if !p.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (p printer) ok(s string)   { p.line(p.paint("1;32", "ok") + "  " + s) }
func (p printer) warn(s string) { p.line(p.paint("1;33", "!") + " " + s) }
