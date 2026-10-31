package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

// runRestoreCmd handles `dockmesh restore --from <path> [flags]`.
// Restores a system backup into the server's DBPath / StacksRoot /
// DataDir, with the server stopped. Uses the same stage-then-swap as the
// UI: nothing live changes until the archive has been extracted and
// checked, and the replaced files are kept.
func runRestoreCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh restore --from <path> [--key <file>] [--force] [--dry-run]")
		os.Exit(2)
	}
	if err := runRestore(args); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		os.Exit(1)
	}
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	from := fs.String("from", "", "path to a system-backup archive (.tar.gz or .tar.gz.age) — required")
	keyFile := fs.String("key", "", "backup key file exported from the original install, for encrypted archives")
	force := fs.Bool("force", false, "restore over a populated install (its files are kept next to the data and stacks directories)")
	dryRun := fs.Bool("dry-run", false, "list what would be restored without writing anything")
	_ = fs.Parse(args)

	if *from == "" {
		return fmt.Errorf("--from is required")
	}
	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}
	f, err := os.Open(*from)
	if err != nil {
		return err
	}
	defer f.Close()
	src, err := plaintextArchive(f, *keyFile)
	if err != nil {
		return err
	}

	if *dryRun {
		counts, err := restore.Extract(context.Background(), src, cfg, restore.Options{DryRun: true})
		if err != nil {
			return err
		}
		fmt.Printf("would restore %d files, %d bytes\n", counts.Files, counts.Bytes)
		return nil
	}
	if err := restore.CheckSafety(cfg, *force); err != nil {
		return err
	}

	counts, result, err := restore.Stage(context.Background(), src, cfg, "cli:"+*from, restore.StageOptions{})
	if result != nil {
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
	}
	if err != nil {
		return err
	}
	if _, err := restore.ApplyStaged(cfg); err != nil {
		return fmt.Errorf("swap in the restored files: %w (run the command again or start the server to resume)", err)
	}
	fmt.Printf("restore complete — %d files, %d bytes\n", counts.Files, counts.Bytes)
	fmt.Printf("  db      → %s\n", cfg.DBPath)
	fmt.Printf("  stacks  → %s\n", cfg.StacksRoot)
	fmt.Println("\nstart the server with `systemctl start dockmesh` (or equivalent)")
	return nil
}

// plaintextArchive returns the archive as plaintext, decrypting it with
// the key file if it is age-encrypted.
func plaintextArchive(f io.Reader, keyFile string) (io.Reader, error) {
	var ids []age.Identity
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, err
		}
		if ids, err = secrets.ParseKeyFile(b); err != nil {
			return nil, fmt.Errorf("--key: %w", err)
		}
	}
	r, err := restore.Plaintext(f, ids)
	if errors.Is(err, restore.ErrEncryptedBackup) {
		return nil, fmt.Errorf("the archive is encrypted; pass the backup key file exported from the original install with --key")
	}
	return r, err
}
