package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dockmesh/dockmesh/internal/restore"
)

// runRestoreCmd handles `dockmesh restore --from <path> [flags]`.
// Extracts a system-backup tarball (produced by the P.6.5 default
// system backup job) into the server's DBPath / StacksRoot / DataDir
// so a fresh host can come up with the pre-incident state. P.12.4.
//
// Core logic lives in internal/restore so the HTTP upload path can
// reuse the same extraction + sanity code.
func runRestoreCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh restore --from <path> [--force] [--dry-run]")
		os.Exit(2)
	}
	if err := runRestore(args); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		os.Exit(1)
	}
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	from := fs.String("from", "", "path to a system-backup tarball (dockmesh-system job output) — required")
	force := fs.Bool("force", false, "overwrite a populated DB / non-empty /stacks / non-empty /data")
	dryRun := fs.Bool("dry-run", false, "list what would be restored without writing anything")
	skipSanity := fs.Bool("skip-sanity", false, "skip the post-restore sanity check (advanced; you own the consequences)")
	_ = fs.Parse(args)

	if *from == "" {
		return fmt.Errorf("--from is required")
	}
	if _, err := os.Stat(*from); err != nil {
		return fmt.Errorf("--from %q: %w", *from, err)
	}
	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}
	src, err := os.Open(*from)
	if err != nil {
		return err
	}
	defer src.Close()

	counts, err := restore.Extract(context.Background(), src, cfg, restore.Options{Force: *force, DryRun: *dryRun})
	if err != nil {
		if errors.Is(err, restore.ErrEncryptedBackup) {
			return err
		}
		return err
	}
	if *dryRun {
		fmt.Printf("would restore %d files, %d bytes\n", counts.Files, counts.Bytes)
		return nil
	}

	fmt.Printf("restore complete — %d files, %d bytes\n", counts.Files, counts.Bytes)
	fmt.Printf("  db      → %s\n", cfg.DBPath)
	fmt.Printf("  stacks  → %s\n", cfg.StacksRoot)

	if *skipSanity {
		fmt.Println("\nsanity check skipped (--skip-sanity). Run 'dockmesh doctor' before serving traffic.")
		return nil
	}

	fmt.Println("\nrunning post-restore sanity checks…")
	result, err := restore.Sanity(cfg)
	if err != nil {
		return fmt.Errorf("post-restore sanity: %w", err)
	}
	for _, c := range result.Checks {
		icon := "[ ok ]"
		switch c.Status {
		case "warn":
			icon = "[warn]"
		case "fail":
			icon = "[FAIL]"
		}
		msg := c.Message
		if msg == "" {
			msg = c.Status
		}
		fmt.Printf("  %s %-20s %s\n", icon, c.Name, msg)
	}
	if !result.Passed {
		return fmt.Errorf("sanity failed: %s", result.Summary)
	}
	fmt.Println("\nsanity OK — start the server with `systemctl start dockmesh` (or equivalent)")
	return nil
}
