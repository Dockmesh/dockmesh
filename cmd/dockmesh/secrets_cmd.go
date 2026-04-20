package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// runSecretsCmd handles `dockmesh secrets <subcommand>`. Only `rotate` is
// implemented in Phase 2 per concept §15.2.
func runSecretsCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh secrets rotate")
		os.Exit(2)
	}
	switch args[0] {
	case "rotate":
		if err := rotateSecrets(); err != nil {
			slog.Error("secrets rotate failed", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func rotateSecrets() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.SecretsEncryptEnv {
		return fmt.Errorf("secrets encryption is disabled (DOCKMESH_SECRETS_ENCRYPT_ENV=false)")
	}

	// Load the current (soon-to-be-old) key.
	oldSvc, err := secrets.New(cfg.SecretsKeyPath, true)
	if err != nil {
		return fmt.Errorf("load current key: %w", err)
	}

	// Move the current key to .old and generate a fresh one in its place.
	if err := oldSvc.ArchiveKey(); err != nil {
		return fmt.Errorf("archive key: %w", err)
	}
	newSvc, err := secrets.New(cfg.SecretsKeyPath, true)
	if err != nil {
		return fmt.Errorf("generate new key: %w", err)
	}

	// Walk every stack and re-encrypt its .env.age.
	stacksMgr, err := stacks.NewManager(cfg.StacksRoot, newSvc)
	if err != nil {
		return fmt.Errorf("stacks manager: %w", err)
	}
	defer stacksMgr.Close()
	count, err := stacksMgr.ReencryptAll(oldSvc)
	if err != nil {
		return fmt.Errorf("reencrypt: %w", err)
	}

	fmt.Printf("rotation complete: %d .env.age files re-encrypted\n", count)
	fmt.Printf("new recipient: %s\n", newSvc.PublicRecipient())
	fmt.Printf("old key archived to: %s.old\n", cfg.SecretsKeyPath)
	fmt.Println()
	fmt.Println("!! If dockmesh is currently running, restart it now:")
	fmt.Println("     systemctl restart dockmesh   (or kill + start)")
	fmt.Println("   Otherwise the running process keeps the OLD key cached")
	fmt.Println("   in memory and every stack read will 500 with")
	fmt.Println("   'decrypt: identity did not match any of the recipients'.")
	fmt.Println("   For live rotation without restart, use the UI button:")
	fmt.Println("     Settings → System → Rotate encryption key")
	fmt.Println()
	fmt.Println("reminder: external backups encrypted with the old key must")
	fmt.Println("be re-encrypted or re-created separately — dockmesh does not")
	fmt.Println("track them yet.")
	return nil
}
