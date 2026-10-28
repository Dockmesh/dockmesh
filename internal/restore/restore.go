// Package restore extracts Dockmesh system-backup tarballs back onto
// disk + runs post-restore sanity checks. Shared code path for the
// `dockmesh restore` CLI (cmd_restore.go) and the
// `POST /api/v1/restore/upload` HTTP handler. Kept package-
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
	"github.com/dockmesh/dockmesh/internal/safepath"
	_ "modernc.org/sqlite"
)

// Counts is what Extract returns on success.
type Counts struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// SkippedLinks counts symlink/hardlink entries. Dockmesh's own
	// backups never contain links, so they are dropped, not followed.
	SkippedLinks int `json:"skipped_links,omitempty"`
}

// Options controls Extract behaviour.
type Options struct {
	Force    bool
	DryRun   bool
	// MaxBytes and MaxEntries cap what an archive may unpack to (0 = no
	// limit). Set for verification, where the archive comes from outside.
	MaxBytes   int64
	MaxEntries int
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

	counts, err := extractAll(ctx, tr, cfg, dbTmp, opts)
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
// Verification unpacks archives into the system temp dir; a small gzip
// stream can expand to far more than it, so the result is capped.
const (
	verifyMaxBytes   = 8 << 30
	verifyMaxEntries = 200_000
)

// ErrTooLarge is returned when an archive exceeds Options.MaxBytes or
// Options.MaxEntries.
var ErrTooLarge = errors.New("archive unpacks to more than the allowed size")

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
	counts, err := Extract(ctx, r, cfg, Options{Force: true, MaxBytes: verifyMaxBytes, MaxEntries: verifyMaxEntries})
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

func extractAll(ctx context.Context, tr *tar.Reader, cfg *config.Config, dbTmp string, opts Options) (*Counts, error) {
	c := &Counts{}
	dataDir := filepath.Dir(cfg.DBPath)
	liveDB := filepath.Base(cfg.DBPath)
	entries := 0
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
		entries++
		if opts.MaxEntries > 0 && entries > opts.MaxEntries {
			return nil, ErrTooLarge
		}
		clean := filepath.ToSlash(filepath.Clean(hdr.Name))
		if strings.Contains(clean, "..") {
			return nil, fmt.Errorf("archive entry escapes root: %q", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			c.SkippedLinks++
			continue
		}
		var root, rel string
		switch {
		case clean == "dockmesh.db":
		case strings.HasPrefix(clean, "stacks/"):
			root, rel = cfg.StacksRoot, strings.TrimPrefix(clean, "stacks/")
		case strings.HasPrefix(clean, "data/"):
			root, rel = dataDir, strings.TrimPrefix(clean, "data/")
			// The live DB is never part of data/ (it is restored from the
			// top-level snapshot via dbTmp); writing it here would bypass
			// the safety check and the atomic swap.
			if base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(rel, "-wal"), "-shm"), "-journal"); base == liveDB || rel == filepath.Base(dbTmp) {
				continue
			}
		default:
			// Unknown prefix — skip silently (future-version archive
			// fields we don't understand).
			continue
		}
		targetPath := dbTmp
		if root != "" {
			if err := os.MkdirAll(root, 0o755); err != nil {
				return nil, err
			}
			// Resolve follows links already on disk, so a pre-existing
			// symlink under the root can't redirect a write elsewhere.
			if targetPath, err = safepath.Resolve(root, rel); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
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
			// DR bootstrap: the user has to pre-seed secrets.age-key (0400)
			// before running restore so we can decrypt the tarball. The
			// tarball also contains secrets.age-key, and overwriting a
			// 0400 file via O_TRUNC fails with EACCES — unlink-then-create
			// makes this idempotent.
			_ = os.Remove(targetPath)
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hdr.Mode&0o777)) //nolint:gosec // masked to permission bits
			if err != nil {
				return nil, fmt.Errorf("create %q: %w", targetPath, err)
			}
			src := io.Reader(tr)
			if opts.MaxBytes > 0 {
				src = io.LimitReader(tr, opts.MaxBytes-c.Bytes+1)
			}
			n, err := io.Copy(out, src)
			out.Close()
			if err != nil {
				return nil, fmt.Errorf("write %q: %w", targetPath, err)
			}
			if opts.MaxBytes > 0 && c.Bytes+n > opts.MaxBytes {
				return nil, ErrTooLarge
			}
			_ = os.Chtimes(targetPath, hdr.ModTime, hdr.ModTime)
			c.Files++
			c.Bytes += n
		}
	}
	return c, nil
}
