package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
)

// runCACmd handles `dockmesh ca <subcommand>`.
func runCACmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh ca <export|rotate|reissue-server> [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "reissue-server":
		if err := caReissueServer(args[1:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "ca reissue-server:", err)
			os.Exit(1)
		}
	case "export":
		if err := caExport(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "ca export:", err)
			os.Exit(1)
		}
	case "rotate":
		if err := caRotate(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "ca rotate:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown ca subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

// caPKIDir returns the directory that holds agents-ca.crt and the
// matching PKI material — same convention main() uses (`filepath.Dir`
// of DBPath so everything follows DOCKMESH_DB_PATH + data-dir
// overrides).
func caPKIDir(dbPath string) string {
	return filepath.Dir(dbPath)
}

func caExport(args []string) error {
	fs := flag.NewFlagSet("ca export", flag.ExitOnError)
	out := fs.String("out", "", "output path (required, e.g. ./ca.pem)")
	_ = fs.Parse(args)

	if *out == "" {
		return fmt.Errorf("--out is required")
	}

	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}

	mgr, err := pki.New(caPKIDir(cfg.DBPath), nil)
	if err != nil {
		return fmt.Errorf("pki load: %w", err)
	}

	if err := os.WriteFile(*out, mgr.CACertPEM(), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote CA cert to %s (%d bytes)\n", *out, len(mgr.CACertPEM()))
	return nil
}

// sanList collects repeated --add-san flags; commas also split.
type sanList []string

func (s *sanList) String() string { return strings.Join(*s, ",") }

func (s *sanList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		*s = append(*s, strings.TrimSpace(part))
	}
	return nil
}

const reissueServerHelp = `usage: dockmesh ca reissue-server --add-san <name-or-ip> [--add-san ...]

Issues a new agent listener certificate (agents-server.crt/.key in the data
dir) signed by the existing agent CA. It keeps every name and IP of the
current certificate and adds the given ones. The old files are kept as
agents-server.{crt,key}.bak-<timestamp>. The CA is unchanged, so enrolled
agents keep trusting the server and do not need to re-enroll.

Moving the server to a new address:
  1. Install dockmesh on the new host and restore the system backup:
       dockmesh restore --from <backup.tar.gz>
  2. Add the new IP and/or hostname to the agent certificate:
       dockmesh ca reissue-server --add-san <new-ip> --add-san <new-name>
  3. In dockmesh.env set DOCKMESH_AGENT_PUBLIC_URL=wss://<new-address>:8443/connect
     (and DOCKMESH_BASE_URL if it names the old address), then start the
     service (systemctl restart dockmesh).
  4. On every agent host set DOCKMESH_AGENT_URL=wss://<new-address>:8443/connect
     in /etc/dockmesh-agent/agent.env (or the container's environment) and
     restart the agent (systemctl restart dockmesh-agent).
  5. Check that every agent shows as online on the Agents page.

Flags:
`

func caReissueServer(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ca reissue-server", flag.ContinueOnError)
	var add sanList
	fs.Var(&add, "add-san", "DNS name or IP address to add (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), reissueServerHelp)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (use --add-san)", fs.Arg(0))
	}
	if len(add) == 0 {
		fs.Usage()
		return fmt.Errorf("--add-san is required")
	}

	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}
	dir := caPKIDir(cfg.DBPath)
	res, err := pki.ReissueServerCert(dir, add, time.Now())
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "agent server certificate in %s\n", dir)
	fmt.Fprintf(out, "  before: %s\n", strings.Join(res.OldSANs, ", "))
	if !res.Changed() {
		fmt.Fprintf(out, "already covers %s, nothing changed\n", strings.Join(res.Present, ", "))
		return nil
	}
	fmt.Fprintf(out, "  after:  %s\n", strings.Join(res.SANs, ", "))
	if len(res.Added) > 0 {
		fmt.Fprintf(out, "  added:  %s\n", strings.Join(res.Added, ", "))
	}
	fmt.Fprintf(out, "  valid until %s\n", res.NotAfter.UTC().Format(time.DateOnly))
	fmt.Fprintf(out, "old certificate kept as %s\n", res.BackupCert)
	if res.BackupKey != "" {
		fmt.Fprintf(out, "old key kept as %s\n", res.BackupKey)
	}
	fmt.Fprintln(out, "\nNext: restart the server so the agent listener loads the new certificate")
	fmt.Fprintln(out, "  (systemctl restart dockmesh, or restart the container).")
	if cfg.AgentPublicURL == "" {
		fmt.Fprintln(out, "Set DOCKMESH_AGENT_PUBLIC_URL to the new wss://<address>:8443/connect URL so new install commands use it.")
	} else {
		fmt.Fprintf(out, "DOCKMESH_AGENT_PUBLIC_URL is %s; update it if it names the old address.\n", cfg.AgentPublicURL)
	}
	if envFile := serverEnvFile(cfg.DBPath); envFile != "" {
		fmt.Fprintf(out, "  (env file: %s)\n", envFile)
	}
	fmt.Fprintln(out, "Then set DOCKMESH_AGENT_URL to the new address on each agent and restart it; no re-enrollment is needed.")
	return nil
}

// caRotate is destructive: it archives the current CA + server cert,
// generates fresh ones, and marks every agent as pending re-enrollment
// with a new token. The operator must then re-run the install command
// on each agent host. We require --reissue-all-agents as a
// safety flag to make the consequences explicit.
func caRotate(args []string) error {
	fs := flag.NewFlagSet("ca rotate", flag.ExitOnError)
	confirm := fs.Bool("reissue-all-agents", false, "required — confirms that every enrolled agent will need to be re-enrolled")
	yes := fs.Bool("yes", false, "skip interactive confirmation prompt")
	_ = fs.Parse(args)

	if !*confirm {
		return fmt.Errorf("--reissue-all-agents is required. Rotating the CA invalidates every agent cert — they will all need to re-enroll")
	}

	cfg, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	dir := caPKIDir(cfg.DBPath)

	// Show blast radius before touching anything.
	var agentCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&agentCount); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Rotating CA in %s. %d agent(s) will be marked pending.\n", dir, agentCount)

	if !*yes {
		fmt.Fprint(os.Stderr, "Proceed? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return fmt.Errorf("aborted")
		}
	}

	// Archive old material. Keep .old files so operators can recover
	// if they've misjudged the blast radius.
	for _, name := range []string{"agents-ca.crt", "agents-ca.key", "agents-server.crt", "agents-server.key"} {
		src := filepath.Join(dir, name)
		dst := src + ".old"
		if _, err := os.Stat(src); err == nil {
			_ = os.Remove(dst)
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("archive %s: %w", name, err)
			}
		}
	}

	// Generate new CA + server cert. We re-use the SAN list from env
	// (same derivation main() does) so the new server cert covers the
	// same hostnames/IPs.
	sans := []string{}
	if cfg.AgentSANs != "" {
		for _, s := range strings.Split(cfg.AgentSANs, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				sans = append(sans, s)
			}
		}
	}
	mgr, err := pki.New(dir, sans)
	if err != nil {
		return fmt.Errorf("pki regen: %w", err)
	}

	agentPublic := cfg.AgentPublicURL
	if agentPublic == "" {
		agentPublic = deriveAgentURL(cfg.BaseURL, cfg.AgentListen)
	}
	svc := agents.NewService(database, mgr, cfg.BaseURL, agentPublic)
	svc.SetEnrollTokenTTL(cfg.AgentEnrollTokenTTL)
	touched, err := svc.ReissueAllEnrollTokens(context.Background())
	if err != nil {
		return err
	}

	fmt.Printf("new CA recipient: fingerprint-cert in %s/agents-ca.crt\n", dir)
	fmt.Printf("old material archived with .old suffix\n\n")
	fmt.Printf("%d agent(s) now pending. Re-enrol each with its fresh token:\n\n", len(touched))
	for _, e := range touched {
		fmt.Printf("  %-30s %s\n", e.Agent.Name, e.InstallHint)
	}
	return nil
}
