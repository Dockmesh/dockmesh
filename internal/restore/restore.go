// Package restore extracts Dockmesh system-backup tarballs back onto
// disk + runs post-restore sanity checks. Shared code path for the
// `dockmesh restore` CLI (cmd_restore.go) and the
// `POST /api/v1/restore/upload` HTTP handler (P.12.4). Kept package-
// free of CLI concerns (flag parsing, stdout progress) so both
// callers can plug their own presentation.
package restore

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
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

// Counts is what Extract returns on success.
type Counts struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Options controls Extract behaviour.
type Options struct {
	Force    bool
	DryRun   bool
	TmpBase  string // where to stage the DB temp file; default: same dir as cfg.DBPath
}

// SanityResult is the structured output of the post-restore checks.
// Exported so HTTP handlers can serialise it back to the UI.
type SanityResult struct {
	Checks  []SanityCheck `json:"checks"`
	Passed  bool          `json:"passed"`
	Summary string        `json:"summary"`
}

type SanityCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok | warn | fail
	Message string `json:"message,omitempty"`
}

// ErrEncryptedBackup is returned when the tarball looks age-encrypted.
// The server's own age key is inside the tarball, so self-encrypted
// system backups can't self-restore — callers must decrypt externally.
var ErrEncryptedBackup = errors.New("tarball is age-encrypted; decrypt externally first (server's own key is inside the tarball, so encrypted system backups cannot self-restore)")

// Extract reads a gzipped tar from r and writes its contents to the
// paths referenced by cfg. Returns file + byte counts. The DB is
// written to a temp file and atomically renamed; the pre-existing
// DB (if any) is archived to <dbpath>.pre-restore-<epoch>.
//
// When opts.DryRun is true, nothing is written — Extract just walks
// the archive to validate it + count entries.
func Extract(ctx context.Context, r io.Reader, cfg *config.Config, opts Options) (*Counts, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	peek, _ := br.Peek(32)
	if strings.HasPrefix(string(peek), "age-encryption.org/v1") {
		return nil, ErrEncryptedBackup
	}
	gzr, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	if opts.DryRun {
		return walkTarDryRun(tr)
	}

	if err := CheckSafety(cfg, opts.Force); err != nil {
		return nil, err
	}

	tmpBase := opts.TmpBase
	if tmpBase == "" {
		tmpBase = filepath.Dir(cfg.DBPath)
	}
	dbTmp := filepath.Join(tmpBase, filepath.Base(cfg.DBPath)+".restore.tmp")
	_ = os.Remove(dbTmp)

	counts, err := extractAll(ctx, tr, cfg, dbTmp)
	if err != nil {
		_ = os.Remove(dbTmp)
		return nil, err
	}

	// Atomic DB swap with pre-restore archive for undo.
	if _, err := os.Stat(cfg.DBPath); err == nil {
		backup := fmt.Sprintf("%s.pre-restore-%d", cfg.DBPath, time.Now().Unix())
		if err := os.Rename(cfg.DBPath, backup); err != nil {
			_ = os.Remove(dbTmp)
			return nil, fmt.Errorf("archive pre-restore db: %w", err)
		}
	}
	if err := os.Rename(dbTmp, cfg.DBPath); err != nil {
		return nil, fmt.Errorf("finalise db: %w", err)
	}
	return counts, nil
}

// ExtractToTemp is like Extract but writes into an ephemeral directory
// tree instead of the live paths. Used by Verify — extract a backup
// into /tmp/verify-<id>/ and run Sanity against those paths without
// touching production. Caller must os.RemoveAll the returned dir.
func ExtractToTemp(ctx context.Context, r io.Reader, label string) (dir string, cfg *config.Config, _ *Counts, err error) {
	dir, err = os.MkdirTemp("", "dockmesh-verify-"+label+"-")
	if err != nil {
		return "", nil, nil, err
	}
	// Synthesise a config that points into the temp dir.
	cfg = &config.Config{
		DBPath:           filepath.Join(dir, "dockmesh.db"),
		StacksRoot:       filepath.Join(dir, "stacks"),
		SecretsPath:      filepath.Join(dir, "secrets.env"),
		SecretsKeyPath:   filepath.Join(dir, "secrets.age-key"),
		AuditGenesisPath: filepath.Join(dir, "audit-genesis.sha256"),
	}
	counts, err := Extract(ctx, r, cfg, Options{Force: true})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, nil, err
	}
	return dir, cfg, counts, nil
}

// CheckSafety refuses to overwrite a populated DB unless opts.Force
// is set. "Populated" = at least one row in users.
func CheckSafety(cfg *config.Config, force bool) error {
	if _, err := os.Stat(cfg.DBPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if force {
		return nil
	}
	database, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return fmt.Errorf("db %q exists and could not be opened (server running? stop it first): %w", cfg.DBPath, err)
	}
	defer database.Close()
	var users int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err == nil && users > 0 {
		return fmt.Errorf("db %q already has %d users — refusing to restore over a populated install. Use force to overwrite; the existing DB will be moved to %s.pre-restore-<ts>", cfg.DBPath, users, cfg.DBPath)
	}
	return nil
}

// Sanity runs the post-restore health checks against the paths
// referenced by cfg. Opens + migrates the DB, verifies at least one
// admin exists, and warns (not fails) on missing CA / genesis /
// secrets. Returns ok=false when any check fails (not warns).
func Sanity(cfg *config.Config) (*SanityResult, error) {
	r := &SanityResult{Passed: true}

	// 1. DB opens + migrations current.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		r.Passed = false
		r.Checks = append(r.Checks, SanityCheck{Name: "db.open", Status: "fail", Message: err.Error()})
		r.Summary = "db could not be opened"
		return r, nil
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		r.Passed = false
		r.Checks = append(r.Checks, SanityCheck{Name: "db.migrate", Status: "fail", Message: err.Error()})
		r.Summary = "migrations failed"
		return r, nil
	}
	r.Checks = append(r.Checks, SanityCheck{Name: "db.open", Status: "ok", Message: "opens + migrations current"})

	// 2. Admin user present.
	var adminCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&adminCount); err != nil {
		r.Passed = false
		r.Checks = append(r.Checks, SanityCheck{Name: "users.admin", Status: "fail", Message: err.Error()})
		r.Summary = "failed to query users"
		return r, nil
	}
	if adminCount == 0 {
		r.Passed = false
		r.Checks = append(r.Checks, SanityCheck{Name: "users.admin", Status: "fail", Message: "no admin users — not bootable"})
		r.Summary = "no admin users"
		return r, nil
	}
	r.Checks = append(r.Checks, SanityCheck{Name: "users.admin", Status: "ok", Message: fmt.Sprintf("%d admin(s)", adminCount)})

	// 3-6: file presence (warns, not fails).
	dataDir := filepath.Dir(cfg.DBPath)
	checkFile := func(name, path, consequence string) {
		if _, err := os.Stat(path); err != nil {
			r.Checks = append(r.Checks, SanityCheck{Name: name, Status: "warn", Message: consequence})
		} else {
			r.Checks = append(r.Checks, SanityCheck{Name: name, Status: "ok"})
		}
	}
	checkFile("ca.cert", filepath.Join(dataDir, "agents-ca.crt"), "every agent will need to re-enroll")
	checkFile("ca.key", filepath.Join(dataDir, "agents-ca.key"), "every agent will need to re-enroll")
	if cfg.AuditGenesisPath != "" {
		checkFile("audit.genesis", cfg.AuditGenesisPath, "chain-of-custody claim breaks across the restore boundary")
	}
	if cfg.SecretsPath != "" {
		checkFile("secrets.env", cfg.SecretsPath, "all existing sessions invalidated — users must log in again")
	}
	if info, err := os.Stat(cfg.StacksRoot); err != nil {
		r.Checks = append(r.Checks, SanityCheck{Name: "stacks.root", Status: "warn", Message: "not present (fresh hosts with no stacks are OK)"})
	} else if info.IsDir() {
		r.Checks = append(r.Checks, SanityCheck{Name: "stacks.root", Status: "ok"})
	}
	r.Summary = "all critical checks passed"
	return r, nil
}

// -----------------------------------------------------------------------------
// internal helpers
// -----------------------------------------------------------------------------

func walkTarDryRun(tr *tar.Reader) (*Counts, error) {
	c := &Counts{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		if strings.Contains(clean, "..") {
			return nil, fmt.Errorf("archive entry escapes root: %q", hdr.Name)
		}
		c.Files++
		c.Bytes += hdr.Size
	}
	return c, nil
}

func extractAll(ctx context.Context, tr *tar.Reader, cfg *config.Config, dbTmp string) (*Counts, error) {
	c := &Counts{}
	dataDir := filepath.Dir(cfg.DBPath)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		if strings.Contains(clean, "..") {
			return nil, fmt.Errorf("archive entry escapes root: %q", hdr.Name)
		}
		var targetPath string
		switch {
		case clean == "dockmesh.db":
			targetPath = dbTmp
		case strings.HasPrefix(clean, "stacks/"):
			targetPath = filepath.Join(cfg.StacksRoot, strings.TrimPrefix(clean, "stacks/"))
		case strings.HasPrefix(clean, "data/"):
			targetPath = filepath.Join(dataDir, strings.TrimPrefix(clean, "data/"))
		default:
			// Unknown prefix — skip silently (future-version archive
			// fields we don't understand).
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return nil, fmt.Errorf("mkdir %q: %w", targetPath, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return nil, err
			}
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return nil, fmt.Errorf("create %q: %w", targetPath, err)
			}
			n, err := io.Copy(out, tr)
			out.Close()
			if err != nil {
				return nil, fmt.Errorf("write %q: %w", targetPath, err)
			}
			_ = os.Chtimes(targetPath, hdr.ModTime, hdr.ModTime)
			c.Files++
			c.Bytes += n
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(targetPath), 0o755)
			_ = os.Remove(targetPath)
			if err := os.Symlink(hdr.Linkname, targetPath); err != nil {
				return nil, fmt.Errorf("symlink %q → %q: %w", targetPath, hdr.Linkname, err)
			}
			c.Files++
		}
	}
	return c, nil
}
