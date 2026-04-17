package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/pki"
)

type checkResult struct {
	name    string
	ok      bool
	detail  string
	warning bool
}

func ok(name, detail string) checkResult    { return checkResult{name, true, detail, false} }
func fail(name, detail string) checkResult  { return checkResult{name, false, detail, false} }
func warn(name, detail string) checkResult  { return checkResult{name, false, detail, true} }

// runDoctorCmd handles `dockmesh doctor`.
func runDoctorCmd(args []string) {
	_ = flag.NewFlagSet("doctor", flag.ExitOnError).Parse(args)

	checks := []checkResult{}
	checks = append(checks, checkConfig()...)
	checks = append(checks, checkDB()...)
	checks = append(checks, checkDocker())
	checks = append(checks, checkPKI()...)
	checks = append(checks, checkFileWritable()...)

	// Report.
	fmt.Println("dockmesh doctor — health checks")
	fmt.Println()
	var failed, warned int
	for _, c := range checks {
		switch {
		case c.ok:
			fmt.Printf("  [ ok ] %-24s %s\n", c.name, c.detail)
		case c.warning:
			fmt.Printf("  [warn] %-24s %s\n", c.name, c.detail)
			warned++
		default:
			fmt.Printf("  [FAIL] %-24s %s\n", c.name, c.detail)
			failed++
		}
	}
	fmt.Println()
	switch {
	case failed > 0:
		fmt.Printf("%d check(s) failed, %d warning(s). See above.\n", failed, warned)
		os.Exit(1)
	case warned > 0:
		fmt.Printf("All critical checks passed (%d warning(s)).\n", warned)
	default:
		fmt.Println("All checks passed.")
	}
}

func checkConfig() []checkResult {
	cfg, err := cliLoadConfig()
	if err != nil {
		return []checkResult{fail("config load", err.Error())}
	}
	out := []checkResult{ok("config load", fmt.Sprintf("HTTPAddr=%s, BaseURL=%s", cfg.HTTPAddr, cfg.BaseURL))}
	if cfg.AgentPublicURL == "" {
		out = append(out, warn("agent public url", "DOCKMESH_AGENT_PUBLIC_URL not set — derived URL may not be reachable from agent hosts"))
	} else {
		out = append(out, ok("agent public url", cfg.AgentPublicURL))
	}
	return out
}

func checkDB() []checkResult {
	cfg, database, err := loadCLIDB()
	if err != nil {
		return []checkResult{fail("database", err.Error())}
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		return []checkResult{fail("database", "ping: "+err.Error())}
	}

	var users int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return []checkResult{fail("database", "query users: "+err.Error())}
	}

	// Disk space: just report whether the DB's parent dir exists.
	dir := filepath.Dir(cfg.DBPath)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return []checkResult{
			ok("database", fmt.Sprintf("%d user(s)", users)),
			warn("data directory", dir+" missing or not a directory"),
		}
	}
	return []checkResult{
		ok("database", fmt.Sprintf("%d user(s)", users)),
		ok("data directory", dir),
	}
}

func checkDocker() checkResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cli, err := docker.New(ctx)
	if err != nil {
		return warn("docker daemon", "unreachable — container features disabled: "+err.Error())
	}
	defer cli.Close()
	// Cheap round-trip to verify the socket answers.
	if _, err := cli.Raw().ServerVersion(ctx); err != nil {
		return warn("docker daemon", "reachable but ServerVersion failed: "+err.Error())
	}
	v, _ := cli.Raw().ServerVersion(ctx)
	return ok("docker daemon", fmt.Sprintf("API %s, engine %s", v.APIVersion, v.Version))
}

func checkPKI() []checkResult {
	cfg, err := cliLoadConfig()
	if err != nil {
		return []checkResult{fail("pki", err.Error())}
	}
	dir := caPKIDir(cfg.DBPath)
	mgr, err := pki.New(dir, nil)
	if err != nil {
		return []checkResult{fail("pki", err.Error())}
	}
	_ = mgr
	return []checkResult{ok("pki", "CA + server cert loaded from "+dir)}
}

func checkFileWritable() []checkResult {
	cfg, err := cliLoadConfig()
	if err != nil {
		return []checkResult{fail("filesystem", err.Error())}
	}
	dir := filepath.Dir(cfg.DBPath)
	probe := filepath.Join(dir, ".dockmesh-doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return []checkResult{fail("data dir writable", dir+": "+err.Error())}
	}
	_ = os.Remove(probe)
	return []checkResult{ok("data dir writable", dir)}
}
