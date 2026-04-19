package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	_ "modernc.org/sqlite"
)

// runRestoreCmd handles `dockmesh restore --from <path> [flags]`.
// Extracts a system-backup tarball (produced by the P.6.5 default
// system backup job) into the server's DBPath / StacksRoot / DataDir
// so a fresh host can come up with the pre-incident state. P.12.4.
//
// Safety model:
//   - Refuses to run while a Dockmesh server is up (PID file check is
//     future work; for now we check that the DB file isn't locked).
//   - Refuses to overwrite a populated DB unless --force is explicit.
//   - `--dry-run` prints the extracted layout + sizes and makes no
//     changes — first thing to run when you're not sure.
//   - Writes the DB to a temp path then atomic-renames over the target
//     so an interrupted restore doesn't leave half a DB file.
func runRestoreCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh restore --from <path> [--force] [--dry-run]")
		os.Exit(2)
	}
	if err := restore(args); err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err)
		os.Exit(1)
	}
}

type restoreFlags struct {
	from    string
	force   bool
	dryRun  bool
	skipSanity bool
}

func restore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	var f restoreFlags
	fs.StringVar(&f.from, "from", "", "path to a system-backup tarball (dockmesh-system job output) — required")
	fs.BoolVar(&f.force, "force", false, "overwrite a populated DB / non-empty /stacks / non-empty /data")
	fs.BoolVar(&f.dryRun, "dry-run", false, "list what would be restored without writing anything")
	fs.BoolVar(&f.skipSanity, "skip-sanity", false, "skip the post-restore sanity check (advanced; you own the consequences)")
	_ = fs.Parse(args)

	if f.from == "" {
		return fmt.Errorf("--from is required")
	}
	if _, err := os.Stat(f.from); err != nil {
		return fmt.Errorf("--from %q: %w", f.from, err)
	}

	cfg, err := cliLoadConfig()
	if err != nil {
		return err
	}

	// Open the tarball, sniff for age-encryption magic.
	src, err := os.Open(f.from)
	if err != nil {
		return err
	}
	defer src.Close()

	// age-encrypted files start with the ASCII preamble
	// "age-encryption.org/v1". We peek the first 32 bytes and reset.
	br := bufio.NewReaderSize(src, 64*1024)
	peek, _ := br.Peek(32)
	if strings.HasPrefix(string(peek), "age-encryption.org/v1") {
		return fmt.Errorf("tarball is age-encrypted; decrypt externally first (the server's own key is inside the tarball, so encrypted system backups cannot self-restore — use unencrypted system backups for DR)")
	}

	gzr, err := gzip.NewReader(br)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	if f.dryRun {
		return dryRunList(tr, cfg)
	}

	// Safety gates — make sure we don't clobber an existing install.
	if err := checkRestoreSafety(cfg, f.force); err != nil {
		return err
	}

	// Stage output paths. DB goes to a temp file first so an interrupted
	// extract doesn't leave a half-written DB that the post-restore
	// sanity would refuse anyway but confuses the operator.
	dbTmp := cfg.DBPath + ".restore.tmp"
	_ = os.Remove(dbTmp)

	written, err := extractAll(tr, cfg, dbTmp)
	if err != nil {
		_ = os.Remove(dbTmp)
		return err
	}

	// Atomic DB swap. If the old DB existed (force mode), we archive it
	// to .pre-restore-<timestamp> so the operator has an undo option.
	if _, err := os.Stat(cfg.DBPath); err == nil {
		backup := fmt.Sprintf("%s.pre-restore-%d", cfg.DBPath, time.Now().Unix())
		if err := os.Rename(cfg.DBPath, backup); err != nil {
			_ = os.Remove(dbTmp)
			return fmt.Errorf("archive pre-restore db: %w", err)
		}
		fmt.Fprintf(os.Stderr, "pre-restore DB moved to %s\n", backup)
	}
	if err := os.Rename(dbTmp, cfg.DBPath); err != nil {
		return fmt.Errorf("finalise db: %w", err)
	}

	fmt.Printf("restore complete — %d files, %d bytes\n", written.count, written.bytes)
	fmt.Printf("  db      → %s\n", cfg.DBPath)
	fmt.Printf("  stacks  → %s\n", cfg.StacksRoot)
	fmt.Printf("  data    → %s\n", filepath.Dir(cfg.DBPath))

	if f.skipSanity {
		fmt.Println("\nsanity check skipped (--skip-sanity). Run 'dockmesh doctor' before serving traffic.")
		return nil
	}

	fmt.Println()
	if err := postRestoreSanity(cfg); err != nil {
		return fmt.Errorf("post-restore sanity: %w", err)
	}
	fmt.Println("\nsanity OK — start the server with `systemctl start dockmesh` (or equivalent)")
	return nil
}

type counts struct {
	count int
	bytes int64
}

// checkRestoreSafety refuses to run against an existing populated
// install unless --force is set. The definition of "populated" is:
// the DB file exists AND contains at least one row in the users
// table. Empty-but-existing DB is treated as fresh (counts as "OK
// to restore into").
func checkRestoreSafety(cfg *config.Config, force bool) error {
	if _, err := os.Stat(cfg.DBPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if force {
		return nil
	}
	database, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		// DB file exists but we can't open it — could be locked by a
		// running server, or a different driver. Safer to refuse.
		return fmt.Errorf("db %q exists and could not be opened (server running? try `systemctl stop dockmesh` first): %w", cfg.DBPath, err)
	}
	defer database.Close()
	var users int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err == nil && users > 0 {
		return fmt.Errorf("db %q already has %d users — refusing to restore over a populated install. Use --force if this is intentional; the existing DB will be moved to %s.pre-restore-<ts>", cfg.DBPath, users, cfg.DBPath)
	}
	return nil
}

// extractAll walks the tarball and writes each entry to its target.
// `dockmesh.db` → dbTmp; `stacks/…` → cfg.StacksRoot; `data/…` →
// same dir as cfg.DBPath. Any other prefix is rejected as an
// unexpected archive shape.
func extractAll(tr *tar.Reader, cfg *config.Config, dbTmp string) (counts, error) {
	var c counts
	dataDir := filepath.Dir(cfg.DBPath)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c, err
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		// Guard against tar-slip: reject entries that contain
		// backtracks after clean.
		if strings.Contains(clean, "..") {
			return c, fmt.Errorf("archive entry escapes root: %q", hdr.Name)
		}

		var targetPath string
		switch {
		case clean == "dockmesh.db":
			targetPath = dbTmp
		case strings.HasPrefix(clean, "stacks/"):
			rel := strings.TrimPrefix(clean, "stacks/")
			targetPath = filepath.Join(cfg.StacksRoot, rel)
		case strings.HasPrefix(clean, "data/"):
			rel := strings.TrimPrefix(clean, "data/")
			targetPath = filepath.Join(dataDir, rel)
		default:
			// Unknown prefix — probably a backup from a future version
			// or a hand-rolled archive. Warn + skip instead of failing,
			// so a partially-compatible backup still gets us most of
			// the way there.
			fmt.Fprintf(os.Stderr, "warning: skipping unknown archive entry %q\n", hdr.Name)
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return c, fmt.Errorf("mkdir %q: %w", targetPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return c, err
			}
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return c, fmt.Errorf("create %q: %w", targetPath, err)
			}
			n, err := io.Copy(out, tr)
			out.Close()
			if err != nil {
				return c, fmt.Errorf("write %q: %w", targetPath, err)
			}
			// Preserve mtime so audit timelines aren't corrupted
			// by restore timestamps.
			_ = os.Chtimes(targetPath, hdr.ModTime, hdr.ModTime)
			c.count++
			c.bytes += n
		case tar.TypeSymlink:
			// Stacks can contain symlinks. Recreate them as-is.
			_ = os.MkdirAll(filepath.Dir(targetPath), 0o755)
			_ = os.Remove(targetPath)
			if err := os.Symlink(hdr.Linkname, targetPath); err != nil {
				return c, fmt.Errorf("symlink %q → %q: %w", targetPath, hdr.Linkname, err)
			}
			c.count++
		default:
			// Ignore device nodes, hard links, etc. — not expected in
			// Dockmesh backups.
		}
	}
	return c, nil
}

// dryRunList walks the tarball without writing, reporting each entry
// + its mapped target path + size. Useful to audit what a backup
// contains before trusting it.
func dryRunList(tr *tar.Reader, cfg *config.Config) error {
	dataDir := filepath.Dir(cfg.DBPath)
	var total int64
	var files int
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		var target string
		switch {
		case clean == "dockmesh.db":
			target = cfg.DBPath
		case strings.HasPrefix(clean, "stacks/"):
			target = filepath.Join(cfg.StacksRoot, strings.TrimPrefix(clean, "stacks/"))
		case strings.HasPrefix(clean, "data/"):
			target = filepath.Join(dataDir, strings.TrimPrefix(clean, "data/"))
		default:
			target = "(skipped: " + clean + ")"
		}
		fmt.Printf("  %10d  %s\n", hdr.Size, target)
		total += hdr.Size
		files++
	}
	fmt.Printf("\nwould restore %d files, %d bytes\n", files, total)
	return nil
}

// postRestoreSanity runs the basic checks that tell the operator
// "it's safe to start the server now" vs "don't — something is off".
// Exported via runDoctorPostRestore so `dockmesh doctor` can invoke
// the same logic.
func postRestoreSanity(cfg *config.Config) error {
	fmt.Println("running post-restore sanity checks…")

	// 1. DB opens + migrations current.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	fmt.Println("  [ ok ] db opens + migrations current")

	// 2. At least one admin user.
	var adminCount int
	err = database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&adminCount)
	if err != nil {
		return fmt.Errorf("query users: %w", err)
	}
	if adminCount == 0 {
		return fmt.Errorf("no admin users in restored DB — this backup is not bootable (bootstrap would create a new admin, but the existing state would block it)")
	}
	fmt.Printf("  [ ok ] %d admin user(s) present\n", adminCount)

	// 3. CA cert + key present in data dir. If they're missing, agents
	// cannot authenticate after restore — the operator has to revoke +
	// re-enroll every agent, which is painful but survivable. Warn
	// loudly rather than fail so the operator can decide.
	dataDir := filepath.Dir(cfg.DBPath)
	caCert := filepath.Join(dataDir, "agents-ca.crt")
	caKey := filepath.Join(dataDir, "agents-ca.key")
	for _, p := range []string{caCert, caKey} {
		if _, err := os.Stat(p); err != nil {
			fmt.Printf("  [warn] %s missing — every agent will need to re-enroll after restore\n", filepath.Base(p))
		} else {
			fmt.Printf("  [ ok ] %s present\n", filepath.Base(p))
		}
	}

	// 4. Genesis hash for audit chain. If missing, the chain can't be
	// verified — existing audit rows still insert fine because the
	// genesis file is re-created on first write, but that breaks the
	// chain-of-custody claim. Warn.
	if _, err := os.Stat(cfg.AuditGenesisPath); err != nil {
		fmt.Println("  [warn] audit-genesis.sha256 missing — existing audit rows will remain verifiable against the genesis in the first chained row, but a new genesis file will be written on first boot")
	} else {
		fmt.Println("  [ ok ] audit-genesis.sha256 present")
	}

	// 5. JWT secret. Without it, every user is logged out on first
	// boot (sessions were signed with the old secret and can't verify
	// against a newly-generated one). Warn.
	if _, err := os.Stat(cfg.SecretsPath); err != nil {
		fmt.Println("  [warn] secrets.env (JWT secret) missing — all existing sessions will be invalid; users have to log in again")
	} else {
		fmt.Println("  [ ok ] secrets.env present")
	}

	// 6. Stacks root — expected to exist but may be empty on fresh
	// hosts that had no stacks yet at backup time.
	if info, err := os.Stat(cfg.StacksRoot); err != nil {
		fmt.Printf("  [warn] stacks root %s not present\n", cfg.StacksRoot)
	} else if info.IsDir() {
		fmt.Printf("  [ ok ] stacks root %s present\n", cfg.StacksRoot)
	}
	return nil
}
