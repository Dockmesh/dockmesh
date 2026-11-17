// Package restore extracts Dockmesh system-backup tarballs back onto
// disk + runs post-restore sanity checks. Shared by the `dockmesh
// restore` CLI and the restore, verify and setup HTTP handlers; kept free
// of CLI concerns (flag parsing, stdout progress) so each caller can
// plug its own presentation.
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

	"filippo.io/age"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
	"github.com/dockmesh/dockmesh/internal/stacks"
	_ "modernc.org/sqlite"
)

// Counts is what Extract returns on success.
type Counts struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// SkippedLinks counts symlink/hardlink entries. Dockmesh's own
	// backups never contain links, so they are dropped, not followed.
	SkippedLinks int `json:"skipped_links,omitempty"`
	// LostOwners counts stack entries of other owners that were restored
	// as the restoring account's own because no helper could write them.
	LostOwners int `json:"lost_owners,omitempty"`
}

// Options controls Extract behaviour.
type Options struct {
	Force  bool
	DryRun bool
	// MaxBytes and MaxEntries cap what an archive may unpack to (0 = no
	// limit). Set for verification, where the archive comes from outside.
	MaxBytes   int64
	MaxEntries int
	TmpBase    string // where to stage the DB temp file; default: same dir as cfg.DBPath
	// Files writes stack data of other owners, such as a database
	// directory a container created as uid 999. Nil restores it as the
	// restoring account's own unless that is root.
	Files FileHelper
	// liveOwners maps a destination root to the owner of the live
	// directory it replaces. A restore run as root gives the server's own
	// files that owner instead of root.
	liveOwners map[string]fileOwner
}

type fileOwner struct{ uid, gid int }

// FileHelper writes and removes stack data of other owners, which the
// unprivileged service cannot do itself.
type FileHelper interface {
	EntryWriter(ctx context.Context, base, dir string) EntryWriter
	RemoveAll(ctx context.Context, base, dir string) error
}

// EntryWriter writes entries named relative to its directory with the
// owner, mode and times of their headers.
type EntryWriter interface {
	WriteEntry(hdr *tar.Header, rel string, r io.Reader) error
	Close() error
	Abort()
}

// Helper returns h as a FileHelper, nil for a nil h.
func Helper(h *stackhelper.Helper) FileHelper {
	if h == nil {
		return nil
	}
	return helperFiles{h}
}

type helperFiles struct{ h *stackhelper.Helper }

func (f helperFiles) EntryWriter(ctx context.Context, base, dir string) EntryWriter {
	return f.h.NewWriter(ctx, base, dir)
}

func (f helperFiles) RemoveAll(ctx context.Context, base, dir string) error {
	return f.h.RemoveAll(ctx, base, dir)
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

// Plaintext returns the archive as plaintext, decrypting it with ids if it
// is age-encrypted. ErrEncryptedBackup means it is encrypted and no ids
// were given.
func Plaintext(r io.Reader, ids []age.Identity) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	if !IsAgeEncrypted(br) {
		return br, nil
	}
	if len(ids) == 0 {
		return nil, ErrEncryptedBackup
	}
	return age.Decrypt(br, ids...)
}

// ErrEncryptedBackup is returned when the archive is still age-encrypted.
var ErrEncryptedBackup = errors.New("archive is age-encrypted; supply the backup key file exported from the original install")

// ageHeader starts every age-encrypted file.
const ageHeader = "age-encryption.org/v1"

// IsAgeEncrypted reports whether the buffered stream starts with an age
// header, without consuming it.
func IsAgeEncrypted(br *bufio.Reader) bool {
	peek, _ := br.Peek(len(ageHeader))
	return string(peek) == ageHeader
}

// Extract reads a gzipped tar from r and writes its contents to the
// paths referenced by cfg. Returns file + byte counts. The DB is
// written to a temp file and atomically renamed; the pre-existing
// DB (if any) is archived to <dbpath>.pre-restore-<epoch>.
//
// When opts.DryRun is true, nothing is written — Extract just walks
// the archive to validate it + count entries.
func Extract(ctx context.Context, r io.Reader, cfg *config.Config, opts Options) (*Counts, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	if IsAgeEncrypted(br) {
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
		return fmt.Errorf("db %q already has %d users — refusing to restore over a populated install. Pass --force to replace it; the replaced files are kept in %s", cfg.DBPath, users, filepath.Join(filepath.Dir(cfg.DBPath), PreRestoreDir))
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
	if err := database.QueryRow(`SELECT COUNT(DISTINCT user_id) FROM role_assignments WHERE role_name = 'admin' AND scope_type = 'global'`).Scan(&adminCount); err != nil {
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

// extractAll writes the archive's entries. Directories and files get the
// mode of their header; directory modes are set last, so a read-only
// directory can still be filled. The server that wrote the archive owned
// its database snapshot, so stack entries with another owner were written
// by a container: they go through opts.Files to keep that owner, or get it
// directly when the restore runs as root.
func extractAll(ctx context.Context, tr *tar.Reader, cfg *config.Config, dbTmp string, opts Options) (*Counts, error) {
	c := &Counts{}
	dataDir := filepath.Dir(cfg.DBPath)
	liveDB := filepath.Base(cfg.DBPath)
	entries := 0
	roots := rootSet{}
	defer roots.close()
	var owner *int
	asRoot := os.Geteuid() == 0
	var dirModes []dirMode
	foreign := &foreignStacks{files: opts.Files, root: cfg.StacksRoot}
	defer foreign.abort()
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
			if owner == nil {
				owner = &hdr.Uid
			}
		case strings.HasPrefix(clean, "stacks/"):
			root, rel = cfg.StacksRoot, strings.TrimPrefix(clean, "stacks/")
		case strings.HasPrefix(clean, "data/") && db.IsSnapshotName("", filepath.Base(clean)):
			// Archives written before backups excluded them can carry
			// pre-migration snapshots with stale secrets.
			continue
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
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg {
			continue
		}
		fsys, name := hostFS(), dbTmp
		if root != "" {
			r, err := roots.open(root)
			if err != nil {
				return nil, err
			}
			fsys, name = rootFS(r), filepath.FromSlash(rel)
		}
		otherOwner := root == cfg.StacksRoot && owner != nil && hdr.Uid != *owner && containerData(rel)
		if root == cfg.StacksRoot {
			viaHelper, err := foreign.take(ctx, rel, otherOwner)
			if err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
			if viaHelper {
				if opts.MaxBytes > 0 && c.Bytes+hdr.Size > opts.MaxBytes {
					return nil, ErrTooLarge
				}
				if err := foreign.write(hdr, rel, tr); err != nil {
					return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
				}
				if hdr.Typeflag != tar.TypeDir {
					c.Files++
					c.Bytes += hdr.Size
				}
				continue
			}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := fsys.mkdirAll(name, 0o750); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
			dirModes = append(dirModes, dirMode{fsys, name, restoredMode(hdr, otherOwner, 0o1777)})
		case tar.TypeReg:
			if err := fsys.mkdirAll(filepath.Dir(name), 0o750); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
			// DR bootstrap: the user has to pre-seed secrets.age-key (0400)
			// before running restore so we can decrypt the tarball. The
			// tarball also contains secrets.age-key, and overwriting a
			// 0400 file via O_TRUNC fails with EACCES — unlink-then-create
			// makes this idempotent.
			_ = fsys.remove(name)
			out, err := fsys.openFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hdr.Mode&0o777)) //nolint:gosec // masked to permission bits
			if err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
			src := io.Reader(tr)
			if opts.MaxBytes > 0 {
				src = io.LimitReader(tr, opts.MaxBytes-c.Bytes+1)
			}
			n, err := io.Copy(out, src)
			if err == nil {
				// Set on the descriptor: the umask must not narrow the
				// archive's mode, and no name can be swapped in between.
				err = out.Chmod(restoredMode(hdr, otherOwner, 0o777))
			}
			out.Close()
			if err != nil {
				return nil, fmt.Errorf("write %q: %w", hdr.Name, err)
			}
			if opts.MaxBytes > 0 && c.Bytes+n > opts.MaxBytes {
				return nil, ErrTooLarge
			}
			_ = fsys.chtimes(name, hdr.ModTime, hdr.ModTime)
			c.Files++
			c.Bytes += n
		}
		switch live, known := opts.liveOwners[ownerKey(root, dataDir)]; {
		case otherOwner && !asRoot:
			c.LostOwners++
		case otherOwner:
			if err := fsys.lchown(name, hdr.Uid, hdr.Gid); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
		case asRoot && known:
			if err := fsys.lchown(name, live.uid, live.gid); err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
			}
		}
	}
	if err := foreign.close(); err != nil {
		return nil, fmt.Errorf("restore stack data of other owners: %w", err)
	}
	for i := len(dirModes) - 1; i >= 0; i-- {
		if err := dirModes[i].apply(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// ownerKey is the liveOwners key of an entry's root; the database snapshot
// ends up in the data directory.
func ownerKey(root, dataDir string) string {
	if root == "" {
		return dataDir
	}
	return root
}

// dirMode is the mode a restored directory gets once it is filled.
type dirMode struct {
	fsys entryFS
	name string
	mode os.FileMode
}

func (d dirMode) apply() error {
	f, err := d.fsys.openFile(d.name, os.O_RDONLY|openDirNoFollow, 0)
	if err != nil {
		return fmt.Errorf("restore: set the mode of %s: %w", d.name, err)
	}
	defer f.Close()
	return f.Chmod(d.mode)
}

// restoredMode is the mode an entry gets. Container data keeps the
// archive's mode; the server's own files never become writable for other
// accounts, whatever the archive says.
func restoredMode(hdr *tar.Header, otherOwner bool, bits int64) os.FileMode {
	mode := os.FileMode(hdr.Mode & bits) //nolint:gosec // masked to permission bits
	if otherOwner {
		return mode
	}
	return mode &^ 0o022
}

// foreignStacks sends the entries of each stack that belong to another
// owner through the helper, together with everything below a directory
// it wrote: the service could not create files in there. A stack's own
// directory and the files the stacks manager writes stay the service's.
type foreignStacks struct {
	files FileHelper
	root  string
	stack string
	w     EntryWriter
	dirs  []string
	// left are the stacks already done: a system backup writes each
	// stack in one piece, and going back would start another helper.
	left map[string]bool
}

// take reports whether the stacks entry rel goes to the helper and opens
// a writer for its stack when it does.
func (f *foreignStacks) take(ctx context.Context, rel string, otherOwner bool) (bool, error) {
	if f.files == nil {
		return false, nil
	}
	stack, inner, _ := strings.Cut(rel, "/")
	if stack != f.stack {
		if f.left[stack] {
			return false, fmt.Errorf("the archive returns to stack %q after another one", stack)
		}
		if err := f.close(); err != nil {
			return false, err
		}
		if f.stack != "" {
			if f.left == nil {
				f.left = map[string]bool{}
			}
			f.left[f.stack] = true
		}
		f.stack, f.dirs = stack, nil
	}
	if !containerData(rel) || (!otherOwner && !f.below(inner)) {
		return false, nil
	}
	if f.w == nil {
		dir := filepath.Join(f.root, stack)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return false, err
		}
		f.w = f.files.EntryWriter(ctx, f.root, dir)
	}
	return true, nil
}

// containerData reports whether the stacks entry rel may hold data a
// container wrote: anything inside a stack directory except the files the
// stacks manager writes.
func containerData(rel string) bool {
	_, inner, nested := strings.Cut(rel, "/")
	return nested && inner != "" && !stacks.ManagerFile(inner)
}

func (f *foreignStacks) below(inner string) bool {
	for _, d := range f.dirs {
		if strings.HasPrefix(inner, d+"/") {
			return true
		}
	}
	return false
}

func (f *foreignStacks) write(hdr *tar.Header, rel string, r io.Reader) error {
	_, inner, _ := strings.Cut(rel, "/")
	if hdr.Typeflag == tar.TypeDir {
		f.dirs = append(f.dirs, inner)
	}
	return f.w.WriteEntry(hdr, inner, r)
}

func (f *foreignStacks) close() error {
	if f.w == nil {
		return nil
	}
	w := f.w
	f.w = nil
	return w.Close()
}

func (f *foreignStacks) abort() {
	if f.w != nil {
		f.w.Abort()
		f.w = nil
	}
}

// rootSet opens one os.Root per destination directory. Writing through
// it lets the kernel refuse any path, symlinks already on disk included,
// that would leave the directory.
type rootSet map[string]*os.Root

func (rs rootSet) open(dir string) (*os.Root, error) {
	if r, ok := rs[dir]; ok {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	rs[dir] = r
	return r, nil
}

func (rs rootSet) close() {
	for _, r := range rs {
		r.Close()
	}
}

// entryFS is the subset of file operations extraction needs, either on
// an os.Root or (for the DB temp file this package names itself) on the
// host filesystem.
type entryFS struct {
	mkdirAll func(string, os.FileMode) error
	remove   func(string) error
	openFile func(string, int, os.FileMode) (*os.File, error)
	chtimes  func(string, time.Time, time.Time) error
	lchown   func(string, int, int) error
}

func rootFS(r *os.Root) entryFS {
	return entryFS{r.MkdirAll, r.Remove, r.OpenFile, r.Chtimes, r.Lchown}
}

func hostFS() entryFS {
	return entryFS{os.MkdirAll, os.Remove, os.OpenFile, os.Chtimes, os.Lchown}
}
