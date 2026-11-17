package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// runRestoreCmd handles `dockmesh restore --from <path> [flags]`.
// Restores a system backup into the server's DBPath / StacksRoot /
// DataDir, with the server stopped. Uses the same stage-then-swap as the
// UI: nothing live changes until the archive has been extracted and
// checked, and the replaced files are kept.
func runRestoreCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh restore --from <path> [--key <file>] [--force] [--dry-run] [--base-url <url>]")
		os.Exit(2)
	}
	if err := runRestore(args); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		os.Exit(1)
	}
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	from := fs.String("from", "", "path to a system-backup archive (.tar.gz or .tar.gz.age), - for stdin — required")
	keyFile := fs.String("key", "", "backup key file exported from the original install, for encrypted archives")
	keyFD := fs.Int("key-fd", 0, "read the backup key from this open file descriptor (set by setup apply)")
	force := fs.Bool("force", false, "restore over a populated install (its files are kept next to the data and stacks directories)")
	dryRun := fs.Bool("dry-run", false, "list what would be restored without writing anything")
	baseURL := fs.String("base-url", "", "base URL of the restored server, replacing the one in the backup (set by setup apply from address.base_url)")
	_ = fs.Parse(args)

	if *from == "" {
		return fmt.Errorf("--from is required")
	}
	if *baseURL != "" {
		if err := agents.ValidateBaseURL(*baseURL); err != nil {
			return fmt.Errorf("--base-url: %w", err)
		}
	}
	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}
	// "-" reads the archive from stdin: root's setup apply passes a backup
	// the service account may not open itself.
	var f io.ReadCloser = os.Stdin
	if *from != "-" {
		if f, err = os.Open(*from); err != nil {
			return err
		}
	}
	defer f.Close()
	key, err := readBackupKey(*keyFile, *keyFD)
	if err != nil {
		return err
	}
	src, err := plaintextArchive(f, key)
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
	if *baseURL != "" {
		if err := setRestoredBaseURL(context.Background(), cfg, *baseURL); err != nil {
			return fmt.Errorf("restored, but the Base URL was not set: %w (set it under Settings)", err)
		}
		fmt.Printf("  base URL → %s\n", *baseURL)
	}
	fmt.Printf("restore complete — %d files, %d bytes\n", counts.Files, counts.Bytes)
	fmt.Printf("  db      → %s\n", cfg.DBPath)
	fmt.Printf("  stacks  → %s\n", cfg.StacksRoot)
	fmt.Println("\nstart the server with `systemctl start dockmesh` (or equivalent)")
	return nil
}

// setRestoredBaseURL saves the Base URL a setup file states into a
// restored database: the operator's answer wins over the old server's.
// The agent URL follows it unless the old server had set one of its own.
func setRestoredBaseURL(ctx context.Context, cfg *config.Config, baseURL string) error {
	if err := agents.ValidateBaseURL(baseURL); err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	st := settings.NewStore(conn)
	if err := st.Load(ctx); err != nil {
		return err
	}
	return saveSetupURLs(ctx, st, baseURL, cfg.AgentListen)
}

// maxBackupKey bounds a backup key file; a real one is a few hundred bytes.
const maxBackupKey = 64 << 10

// readBackupKey reads the key from path, or from an inherited descriptor:
// root's setup apply passes a key the service account may not open, and
// reopening /dev/fd/N would check its permissions again. Nil when neither
// is given.
func readBackupKey(path string, fd int) ([]byte, error) {
	var r io.Reader
	switch {
	case fd > 2:
		f := os.NewFile(uintptr(fd), "backup key")
		if f == nil {
			return nil, fmt.Errorf("--key-fd %d is not open", fd)
		}
		defer f.Close()
		r = f
	case path != "":
		f, err := os.Open(path) // #nosec G304 -- the operator's key file
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	default:
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r, maxBackupKey+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBackupKey {
		return nil, errors.New("backup key file too large")
	}
	return b, nil
}

// plaintextArchive returns the archive as plaintext, decrypting it with
// the key if it is age-encrypted.
func plaintextArchive(f io.Reader, key []byte) (io.Reader, error) {
	var ids []age.Identity
	if key != nil {
		var err error
		if ids, err = secrets.ParseKeyFile(key); err != nil {
			return nil, fmt.Errorf("backup key: %w", err)
		}
	}
	r, err := restore.Plaintext(f, ids)
	if errors.Is(err, restore.ErrEncryptedBackup) {
		return nil, fmt.Errorf("the archive is encrypted; pass the backup key file exported from the original install with --key")
	}
	return r, err
}
