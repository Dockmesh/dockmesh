package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/layout"
)

// runSystemCmd handles `dockmesh system <subcommand>`: host-level changes
// that need root, run by the installer.
func runSystemCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh system <migrate-layout>")
		os.Exit(2)
	}
	switch args[0] {
	case "migrate-layout":
		if err := systemMigrateLayout(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "system migrate-layout:", err)
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
	g, err := user.LookupGroup("dockmesh")
	if err != nil {
		return 0
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0
	}
	return gid
}
