package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"golang.org/x/term"
)

// cliLoadConfig loads the same config the server uses, without touching
// the database. Used by subcommands that only need paths/URLs (e.g.
// `ca export`, `config show`).
func cliLoadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// newCATOken generates a fresh enrollment token (32 random bytes, b64url).
// Matches the format agents.newToken() produces internally — duplicated
// here rather than exported to keep the agents package surface clean.
func newCATOken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashCAToken mirrors agents.hashToken — SHA-256 hex.
func hashCAToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// loadCLIDB opens the DB + runs pending migrations. Subcommands use
// this so they share the same data path the server boots from.
func loadCLIDB() (*config.Config, *sql.DB, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open db %q: %w", cfg.DBPath, err)
	}
	if err := db.Migrate(database); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	return cfg, database, nil
}

// readPassword reads a password from stdin without echoing. Falls back
// to plain ReadString when stdin is not a terminal (e.g. piped from
// a script). Use trim=true to strip the trailing newline for piped input.
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// printRootHelp is the top-level `dockmesh help` output.
func printRootHelp() {
	fmt.Fprint(os.Stderr, `dockmesh — container management platform

Usage: dockmesh [command] [flags]

Server:
  serve                  Start the HTTP + agent mTLS server (default if no command given)

Admin:
  admin create           Create a user (--username --email --password --role)
  admin reset-password   Reset a user's password (--user)
  admin list-users       List users

Database:
  db migrate             Run pending migrations
  db backup              Atomic SQLite snapshot (--out backup.db)

Agent PKI:
  ca export              Export the agent CA public cert (--out ca.pem)
  ca rotate              Rotate the agent CA (new key; all agents must re-enroll)

Agent enrollment:
  enroll create          Generate a new enrollment token (--name)
  enroll revoke          Revoke an agent (--name or --id)
  enroll list            List agents

Secrets:
  secrets rotate         Rotate the .env.age secrets key

Migration:
  import compose-dir     Import stacks from a directory of compose files
                         (--path ./src [--prefix ...] [--force] [--dry-run])

Disaster recovery:
  restore                Restore DB + /stacks + /data from a system-backup
                         tarball (--from ./backup.tar.gz [--force] [--dry-run])

Diagnostics:
  config show            Print effective config (secrets redacted)
  doctor                 Run health checks (DB, docker, disk, TLS)
  completion bash|zsh|fish   Print shell completion script
  version                Print version and exit
  help                   This message

Run 'dockmesh <command> --help' for flags.
`)
}
