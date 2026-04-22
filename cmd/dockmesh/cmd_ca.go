package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dockmesh/dockmesh/internal/pki"
)

// runCACmd handles `dockmesh ca <subcommand>`.
func runCACmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh ca <export|rotate> [flags]")
		os.Exit(2)
	}
	switch args[0] {
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

	// Mark all agents pending with fresh tokens. We do it in a single
	// transaction so a partial failure doesn't leave half the fleet
	// re-tokenised.
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, name FROM agents`)
	if err != nil {
		return err
	}
	type entry struct {
		id, name, token string
	}
	var touched []entry
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		tok, err := newCATOken()
		if err != nil {
			rows.Close()
			return err
		}
		touched = append(touched, entry{id: id, name: name, token: tok})
	}
	rows.Close()

	for _, e := range touched {
		hash := hashCAToken(e.token)
		if _, err := tx.Exec(`
			UPDATE agents
			   SET enrollment_token_hash = ?,
			       cert_fingerprint = NULL,
			       status = 'pending',
			       updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`, hash, e.id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Printf("new CA recipient: fingerprint-cert in %s/agents-ca.crt\n", dir)
	fmt.Printf("old material archived with .old suffix\n\n")
	fmt.Printf("%d agent(s) now pending. Re-enrol each with its fresh token:\n\n", len(touched))
	for _, e := range touched {
		fmt.Printf("  %-30s curl -fsSL %s/install/agent.sh?token=%s | sudo bash\n",
			e.name, cfg.BaseURL, e.token)
	}
	_ = mgr
	return nil
}
