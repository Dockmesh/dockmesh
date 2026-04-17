package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/pki"
)

// runEnrollCmd handles `dockmesh enroll <subcommand>`.
func runEnrollCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh enroll <create|revoke|list> [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		if err := enrollCreate(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "enroll create:", err)
			os.Exit(1)
		}
	case "revoke":
		if err := enrollRevoke(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "enroll revoke:", err)
			os.Exit(1)
		}
	case "list":
		if err := enrollList(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "enroll list:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown enroll subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func enrollCreate(args []string) error {
	fs := flag.NewFlagSet("enroll create", flag.ExitOnError)
	name := fs.String("name", "", "agent name (required)")
	_ = fs.Parse(args)

	if *name == "" {
		return fmt.Errorf("--name is required")
	}

	cfg, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	// We construct agents.Service the same way main() does. The PKI
	// manager only needs to load — no new material is issued here.
	mgr, err := pki.New(caPKIDir(cfg.DBPath), nil)
	if err != nil {
		return fmt.Errorf("pki load: %w", err)
	}

	agentPublic := cfg.AgentPublicURL
	if agentPublic == "" {
		agentPublic = cfg.BaseURL
	}
	svc := agents.NewService(database, mgr, cfg.BaseURL, agentPublic)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := svc.Create(ctx, *name)
	if err != nil {
		return err
	}

	fmt.Printf("agent created: id=%s name=%s\n", res.Agent.ID, res.Agent.Name)
	fmt.Printf("token: %s\n", res.Token)
	fmt.Printf("\ninstall on the agent host:\n  %s\n", res.InstallHint)
	return nil
}

func enrollRevoke(args []string) error {
	fs := flag.NewFlagSet("enroll revoke", flag.ExitOnError)
	name := fs.String("name", "", "agent name")
	id := fs.String("id", "", "agent id")
	_ = fs.Parse(args)

	if *name == "" && *id == "" {
		return fmt.Errorf("--name or --id is required")
	}

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	targetID := *id
	if targetID == "" {
		err := database.QueryRow(`SELECT id FROM agents WHERE name = ?`, *name).Scan(&targetID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no agent found with name %q", *name)
		}
		if err != nil {
			return err
		}
	}

	res, err := database.Exec(`DELETE FROM agents WHERE id = ?`, targetID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no agent with id %s", targetID)
	}
	fmt.Printf("revoked agent %s\n", targetID)
	return nil
}

func enrollList(args []string) error {
	_ = flag.NewFlagSet("enroll list", flag.ExitOnError).Parse(args)

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	rows, err := database.Query(`
		SELECT id, name, status, COALESCE(hostname, ''), COALESCE(version, ''),
		       last_seen_at
		  FROM agents ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("%-40s  %-20s  %-10s  %-25s  %-15s  %s\n",
		"ID", "NAME", "STATUS", "HOSTNAME", "VERSION", "LAST SEEN")
	var count int
	for rows.Next() {
		var id, name, status, host, version string
		var lastSeen sql.NullTime
		if err := rows.Scan(&id, &name, &status, &host, &version, &lastSeen); err != nil {
			return err
		}
		last := "never"
		if lastSeen.Valid {
			last = lastSeen.Time.Format(time.RFC3339)
		}
		if host == "" {
			host = "-"
		}
		if version == "" {
			version = "-"
		}
		fmt.Printf("%-40s  %-20s  %-10s  %-25s  %-15s  %s\n",
			id, name, status, host, version, last)
		count++
	}
	if count == 0 {
		fmt.Println("(no agents enrolled)")
	}
	return nil
}
