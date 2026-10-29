//go:build composelib

package composelib

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
)

// RunCLI implements `compose-spike <up|down|ps|pull|scale> -dir <stackdir>`.
// The stack is loaded exactly like a Dockmesh deploy (compose.yaml + the
// given env file, no OS env), then handed to the SDK.
func RunCLI(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: compose-spike <up|down|ps|pull|scale> -dir <stackdir> [-name n] [-env file] [-force-recreate] [-wait] [-replicas svc=N]")
		os.Exit(2)
	}
	cmd := args[0]
	fs := flag.NewFlagSet("compose-spike "+cmd, flag.ExitOnError)
	dir := fs.String("dir", ".", "stack directory containing compose.yaml")
	name := fs.String("name", "", "project name (default: directory name)")
	envFile := fs.String("env", "", "env file passed as stack env")
	forceRecreate := fs.Bool("force-recreate", false, "recreate every container")
	forcePull := fs.Bool("pull", false, "pull every image even if cached")
	wait := fs.Bool("wait", false, "wait for running/healthy")
	orphans := fs.Bool("remove-orphans", true, "remove containers of services no longer in compose.yaml")
	replicas := fs.String("replicas", "", "scale: service=N")
	_ = fs.Parse(args[1:])

	abs, err := filepath.Abs(*dir)
	if err != nil {
		fail(err)
	}
	if *name == "" {
		*name = filepath.Base(abs)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	eng, err := New("")
	if err != nil {
		fail(err)
	}
	defer eng.Close()

	opts := Options{OnEvent: printEvent, Log: os.Stderr}
	start := time.Now()

	switch cmd {
	case "ps":
		list, err := eng.Ps(ctx, *name)
		if err != nil {
			fail(err)
		}
		for _, c := range list {
			fmt.Printf("%-30s %-12s %-10s %s\n", c.Name, c.Service, c.State, c.Health)
		}
		return
	case "down":
		err = eng.Down(ctx, *name, *orphans, opts)
	default:
		env := ""
		if *envFile != "" {
			b, rerr := os.ReadFile(*envFile)
			if rerr != nil {
				fail(rerr)
			}
			env = string(b)
		}
		proj, lerr := compose.LoadProject(ctx, abs, *name, env)
		if lerr != nil {
			fail(lerr)
		}
		StampLabels(proj)
		switch cmd {
		case "up":
			err = eng.Up(ctx, proj, UpOptions{
				ForcePull:      *forcePull,
				ForceRecreate:  *forceRecreate,
				RemoveOrphans:  *orphans,
				WaitHealthy:    *wait,
				HealthyTimeout: 2 * time.Minute,
			}, opts)
		case "pull":
			err = eng.Pull(ctx, proj, opts)
		case "scale":
			var svc string
			var n int
			if _, serr := fmt.Sscanf(*replicas, "%[^=]=%d", &svc, &n); serr != nil {
				fail(fmt.Errorf("-replicas: %w", serr))
			}
			err = eng.Scale(ctx, proj, map[string]int{svc: n}, opts)
		default:
			fail(fmt.Errorf("unknown command %q", cmd))
		}
	}
	if err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "%s done in %s\n", cmd, time.Since(start).Round(time.Millisecond))
}

func printEvent(e Event) {
	line := fmt.Sprintf("[%s] %-8s %-40s %s", e.Operation, e.Status, e.ID, e.Text)
	if e.Details != "" {
		line += " (" + e.Details + ")"
	}
	if e.Percent > 0 {
		line += fmt.Sprintf(" %d%%", e.Percent)
	}
	fmt.Fprintln(os.Stderr, line)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
