package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// runDBCmd handles `dockmesh db <subcommand>`.
func runDBCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh db <migrate|backup> [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "migrate":
		if err := dbMigrate(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "db migrate:", err)
			os.Exit(1)
		}
	case "backup":
		if err := dbBackup(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "db backup:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown db subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func dbMigrate(args []string) error {
	_ = flag.NewFlagSet("db migrate", flag.ExitOnError).Parse(args)
	// loadCLIDB already runs migrations — just report success.
	cfg, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()
	fmt.Printf("migrations applied on %s\n", cfg.DBPath)
	return nil
}

// dbBackup uses SQLite's online backup API via `VACUUM INTO` — atomic
// snapshot that works even while the server is running. For Postgres
// we print a note instead (pg_dump belongs to the operator, not us).
func dbBackup(args []string) error {
	fs := flag.NewFlagSet("db backup", flag.ExitOnError)
	out := fs.String("out", "", "output path (required, e.g. ./backup.db)")
	_ = fs.Parse(args)

	if *out == "" {
		return fmt.Errorf("--out is required")
	}

	cfg, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	// VACUUM INTO errors if the target file already exists — require the
	// operator to pick a fresh path or rm the stale one themselves so we
	// don't silently clobber a backup.
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("output path %q already exists; remove it or choose a different path", *out)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// VACUUM INTO requires the path to be a literal string — we can't
	// bind it as a parameter. Escape single quotes defensively even
	// though the operator controls this input.
	safe := strings.ReplaceAll(*out, "'", "''")
	if _, err := database.ExecContext(ctx, "VACUUM INTO '"+safe+"'"); err != nil {
		return fmt.Errorf("vacuum into %q: %w", *out, err)
	}

	info, err := os.Stat(*out)
	if err != nil {
		return err
	}
	fmt.Printf("backup written: %s (%d bytes, took %s) — source: %s\n",
		*out, info.Size(), time.Since(start).Round(time.Millisecond), cfg.DBPath)
	return nil
}
