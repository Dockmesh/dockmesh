package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/layout"
)

// runSystemCmd handles `dockmesh system <subcommand>`: host-level changes
// that need root, run by the installer.
func runSystemCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh system <migrate-layout|write-unit>")
		os.Exit(2)
	}
	switch args[0] {
	case "migrate-layout":
		if err := systemMigrateLayout(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "system migrate-layout:", err)
			os.Exit(1)
		}
	case "write-unit":
		if err := systemWriteUnit(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "system write-unit:", err)
			os.Exit(1)
		}
	case unitPathsCmd:
		if err := systemUnitPaths(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "system "+unitPathsCmd+":", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown system subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func systemMigrateLayout(args []string) error {
	fs := flag.NewFlagSet("system migrate-layout", flag.ExitOnError)
	from := fs.String("from", "", "env file to move (default: the one the dockmesh unit names, then the old default places)")
	_ = fs.Parse(args)
	if runtime.GOOS != "linux" {
		fmt.Println("config stays where it is: the /etc/dockmesh layout is Linux only")
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	candidates := []string{*from}
	if *from == "" {
		candidates = append([]string{unitEnvFile()}, layout.LegacyEnvFiles...)
	}
	res, err := layout.Migrate(layout.MigrateOptions{Candidates: candidates, GID: configGID()})
	if err != nil {
		return err
	}
	switch res.Outcome {
	case layout.Moved:
		fmt.Printf("config moved to %s (symlink left at %s), data stays in %s\n", layout.ConfigFile, res.From, res.DataDir)
	case layout.AlreadyMigrated:
		fmt.Printf("config already at %s\n", layout.ConfigFile)
	case layout.SetupPending:
		fmt.Printf("config stays at %s until the setup wizard has finished\n", res.From)
	case layout.NothingFound:
		fmt.Println("no config found to move")
	}
	return nil
}

// unitEnvFile returns the first EnvironmentFile= of dockmesh.service, empty
// without systemd or the unit.
func unitEnvFile() string {
	out, err := exec.Command("systemctl", "show", "-p", "EnvironmentFiles", "--value", "dockmesh").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[0], "-")
}

// configGID is the group that may read the config: dockmesh, else root.
func configGID() int {
	gid, _ := lookupGID(layout.ServiceGroup)
	return gid
}

func systemWriteUnit(args []string) error {
	fs := flag.NewFlagSet("system write-unit", flag.ExitOnError)
	envFlag := fs.String("env-file", "", "config to write the unit for when "+layout.ConfigFile+" does not exist yet")
	restore := fs.Bool("restore", false, "put back the unit the last write replaced")
	_ = fs.Parse(args)
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd units are only written on Linux")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	if *restore {
		if err := layout.RestoreServerUnit(layout.UnitWriteOptions{}); err != nil {
			return err
		}
		fmt.Printf("restored the earlier %s\n", layout.UnitFile)
		return daemonReload()
	}

	changed, err := writeServerUnit(*envFlag)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("wrote %s (the earlier one is kept as %s)\n", layout.UnitFile, layout.PrevUnitFile)
	} else {
		fmt.Printf("%s is up to date\n", layout.UnitFile)
	}
	return nil
}

// writeServerUnit writes the server's unit for the given config (see
// unitConfigFile) and reloads systemd when it changed.
func writeServerUnit(envFlag string) (bool, error) {
	envFile, err := unitConfigFile(envFlag)
	if err != nil {
		return false, err
	}
	env, err := layout.ReadEnvFile(envFile)
	if err != nil {
		return false, err
	}
	paths := config.ResolvePaths(func(k string) string { return env[k] })
	extra, err := settingsWritablePaths(paths.DBPath)
	if err != nil {
		return false, fmt.Errorf("read the backup directories from %s: %w (the unit was not changed)", paths.DBPath, err)
	}
	binary, err := os.Executable()
	if err != nil {
		return false, err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return false, err
	}
	if err := checkUnitBinary(binary); err != nil {
		return false, fmt.Errorf("%w (the unit was not changed)", err)
	}
	gid, err := ensureServiceGroup()
	if err != nil {
		return false, err
	}
	unit := layout.ServerUnit{
		Binary:              binary,
		EnvFile:             envFile,
		DataDir:             paths.DataDir,
		WritablePaths:       layout.ServerWritablePaths(paths, extra...),
		SupplementaryGroups: supplementaryGroups(envFile, env),
	}
	// Render first: a refused value must leave /etc/dockmesh as it was too.
	if err := layout.CheckServerUnit(unit); err != nil {
		return false, fmt.Errorf("%w (the unit was not changed)", err)
	}
	if err := layout.PrepareConfigDir(gid); err != nil {
		return false, err
	}
	changed, err := layout.WriteServerUnit(unit, layout.UnitWriteOptions{})
	if err != nil {
		return false, fmt.Errorf("%w (the unit was not changed)", err)
	}
	// Without the updater units the server cannot update itself, but it
	// runs as before, so a refused value only warns.
	updaterChanged, uerr := layout.WriteUpdateUnits(binary, paths.DataDir, "")
	if uerr != nil {
		fmt.Fprintln(os.Stderr, "warning: updater units not written:", uerr)
	}
	if changed || updaterChanged {
		if err := daemonReload(); err != nil {
			return changed, err
		}
	}
	if uerr == nil {
		if err := enableUpdater(updaterChanged); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
	}
	return changed, nil
}

// enableUpdater enables the updater's path unit and its boot run, and
// (re)starts the path unit so it watches for requests from now on.
func enableUpdater(restart bool) error {
	path, service := filepath.Base(layout.UpdatePathUnitFile), filepath.Base(layout.UpdateServiceUnitFile)
	if out, err := exec.Command("systemctl", "enable", path, service).CombinedOutput(); err != nil { // #nosec G204 -- constant arguments
		return fmt.Errorf("systemctl enable %s %s: %w: %s", path, service, err, strings.TrimSpace(string(out)))
	}
	verb := "start"
	if restart {
		verb = "restart"
	}
	if out, err := exec.Command("systemctl", verb, path).CombinedOutput(); err != nil { // #nosec G204 -- constant arguments
		return fmt.Errorf("systemctl %s %s: %w: %s", verb, path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unitConfigFile is the env file the unit loads: the root-owned file in
// /etc once it exists, else the given one, the one the unit names today or
// an old default place. A symlink is never named, since its directory may
// belong to the service user.
func unitConfigFile(given string) (string, error) {
	if fi, err := os.Lstat(layout.ConfigFile); err == nil && fi.Mode().IsRegular() {
		return layout.ConfigFile, nil
	}
	for _, c := range append([]string{given, unitEnvFile()}, layout.LegacyEnvFiles...) {
		if c == "" || !filepath.IsAbs(c) {
			continue
		}
		if fi, err := os.Lstat(c); err == nil && fi.Mode().IsRegular() {
			return filepath.Clean(c), nil
		}
	}
	return "", fmt.Errorf("no config found: neither %s nor an earlier env file exists", layout.ConfigFile)
}

// ensureServiceGroup returns the gid of the group the unit runs the
// server with, creating it on a host where useradd made none.
func ensureServiceGroup() (int, error) {
	if gid, ok := lookupGID(layout.ServiceGroup); ok {
		return gid, nil
	}
	if out, err := exec.Command("groupadd", "--system", layout.ServiceGroup).CombinedOutput(); err != nil { // #nosec G204 -- constant arguments
		return 0, fmt.Errorf("create group %s: %w: %s", layout.ServiceGroup, err, strings.TrimSpace(string(out)))
	}
	if gid, ok := lookupGID(layout.ServiceGroup); ok {
		return gid, nil
	}
	return 0, fmt.Errorf("group %s not found after creating it", layout.ServiceGroup)
}

// supplementaryGroups gives the service the docker group even when the
// account was created before Docker was installed, and the group of the
// Docker socket when that is not docker. DOCKER_HOST is only followed when
// it comes from a file only root may write: in the legacy env file it is
// the service account's choice.
func supplementaryGroups(envFile string, env map[string]string) []string {
	dockerGID, ok := lookupGID("docker")
	if !ok {
		dockerGID = -1
	}
	socket := layout.DefaultDockerSocket
	if layout.RootOwnedFile(envFile) {
		socket = layout.DockerSocketPath(env)
	}
	groups, warn := layout.SocketGroups(socket, dockerGID)
	if warn != "" {
		fmt.Fprintln(os.Stderr, "warning:", warn)
	}
	return groups
}

func lookupGID(name string) (int, bool) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(g.Gid)
	return gid, err == nil
}

func daemonReload() error {
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unitPathsCmd is the internal subcommand write-unit runs as the
// database's owner; its output is untrusted and validated by the caller.
const unitPathsCmd = "unit-paths"

type unitPathsOutput struct {
	// RootsSet is false while the allowed backup roots were never saved.
	RootsSet bool     `json:"roots_set"`
	Paths    []string `json:"paths"`
}

func systemUnitPaths(args []string) error {
	fs := flag.NewFlagSet("system "+unitPathsCmd, flag.ExitOnError)
	dbPath := fs.String("db", "", "database to read")
	_ = fs.Parse(args)
	if !filepath.IsAbs(*dbPath) {
		return fmt.Errorf("--db needs an absolute path")
	}
	out, err := readUnitPaths(*dbPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

func readUnitPaths(dbPath string) (unitPathsOutput, error) {
	var out unitPathsOutput
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return out, err
	}
	defer database.Close()
	var n int
	if err := database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'settings'`).Scan(&n); err != nil {
		return out, err
	}
	if n == 0 {
		return out, nil
	}
	rows, err := database.Query(`SELECT key, value FROM settings WHERE key IN (?, ?, ?) ORDER BY key`,
		targets.LocalRootsSetting, targets.EmptyPathSetting, audit.RetentionLocalDir)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		if k == targets.LocalRootsSetting {
			out.RootsSet = true
			// Split as targets.ParseRoots does, without making a relative
			// entry absolute: the caller refuses it instead.
			for _, r := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == ',' }) {
				if r = strings.TrimSpace(r); r != "" {
					out.Paths = append(out.Paths, r)
				}
			}
		} else if v != "" {
			out.Paths = append(out.Paths, v)
		}
	}
	return out, rows.Err()
}
