// Package gitsource keeps a stack's compose.yaml + .env in sync with a
// git repository (P.11.11).
//
// The service clones into ./data/git-cache/<stack>/, pulls on demand
// or via a background poller, and copies the files into the stack's
// filesystem location through the stacks.Manager so the existing deploy
// path sees the change exactly the same as a UI edit would. Optional
// auto-deploy hooks into the deploy service on successful sync.
//
// Credentials at rest are age-encrypted via the shared secrets service,
// matching the pattern used by registries and stack .env files.
package gitsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
	sshauth "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

var (
	ErrNotFound         = errors.New("git source not found")
	ErrInvalidAuthKind  = errors.New("auth_kind must be one of: none, http, ssh")
	ErrPollTooShort     = errors.New("poll_interval_sec must be >= 60")
	ErrComposeMissing   = errors.New("repo did not contain compose.yaml at path_in_repo")
	ErrStackNameInvalid = errors.New("stack name contains characters that cannot be used on disk")
)

// Source is the DB-backed row, returned to handlers with secrets
// redacted via the Has* flags instead of raw values.
type Source struct {
	StackName       string     `json:"stack_name"`
	RepoURL         string     `json:"repo_url"`
	Branch          string     `json:"branch"`
	PathInRepo      string     `json:"path_in_repo"`
	AuthKind        string     `json:"auth_kind"`
	Username        string     `json:"username,omitempty"`
	HasPassword     bool       `json:"has_password"`
	HasSSHKey       bool       `json:"has_ssh_key"`
	AutoDeploy      bool       `json:"auto_deploy"`
	PollIntervalSec int        `json:"poll_interval_sec"`
	HasWebhookSecret bool      `json:"has_webhook_secret"`
	LastSyncSHA     string     `json:"last_sync_sha,omitempty"`
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastSyncError   string     `json:"last_sync_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Input is the CRUD payload. Empty password / ssh_key on update = keep
// existing; use the explicit Clear* booleans to wipe.
type Input struct {
	RepoURL         string `json:"repo_url"`
	Branch          string `json:"branch,omitempty"`
	PathInRepo      string `json:"path_in_repo,omitempty"`
	AuthKind        string `json:"auth_kind,omitempty"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	ClearPassword   bool   `json:"clear_password,omitempty"`
	SSHKey          string `json:"ssh_key,omitempty"`
	ClearSSHKey     bool   `json:"clear_ssh_key,omitempty"`
	AutoDeploy      bool   `json:"auto_deploy,omitempty"`
	PollIntervalSec int    `json:"poll_interval_sec,omitempty"`
	WebhookSecret   string `json:"webhook_secret,omitempty"`
	ClearWebhook    bool   `json:"clear_webhook_secret,omitempty"`
}

// SyncResult records what a sync did — used by handlers and by the
// polling goroutine's auto-deploy decision.
type SyncResult struct {
	OldSHA       string `json:"old_sha,omitempty"`
	NewSHA       string `json:"new_sha"`
	Changed      bool   `json:"changed"`
	Deployed     bool   `json:"deployed,omitempty"`
	DeployResult any    `json:"deploy_result,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
}

// DeployFunc is the callback the service invokes when auto_deploy is on
// and the sync pulled a new SHA. The host id is always "local" for now
// — remote-agent git sources are a follow-up.
type DeployFunc func(ctx context.Context, stackName string) (any, error)

type Service struct {
	db       *sql.DB
	secrets  *secrets.Service
	stacks   *stacks.Manager
	cacheDir string
	deploy   DeployFunc

	stop chan struct{}
	wg   sync.WaitGroup
}

func New(db *sql.DB, secretsSvc *secrets.Service, stacksMgr *stacks.Manager, cacheDir string, deploy DeployFunc) *Service {
	return &Service{
		db:       db,
		secrets:  secretsSvc,
		stacks:   stacksMgr,
		cacheDir: cacheDir,
		deploy:   deploy,
		stop:     make(chan struct{}),
	}
}

// Start launches the polling goroutine. Safe to call without a deploy
// callback — the service just becomes a manual-sync-only surface.
func (s *Service) Start(ctx context.Context) {
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		slog.Warn("gitsource cache dir", "err", err, "path", s.cacheDir)
	}
	s.wg.Add(1)
	go s.pollLoop(ctx)
}

func (s *Service) Stop() {
	close(s.stop)
	s.wg.Wait()
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) Get(ctx context.Context, stackName string) (*Source, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT stack_name, repo_url, branch, path_in_repo, auth_kind,
		       COALESCE(username, ''),
		       password_encrypted IS NOT NULL AS has_password,
		       ssh_key_encrypted IS NOT NULL AS has_ssh_key,
		       auto_deploy, poll_interval_sec,
		       webhook_secret IS NOT NULL AS has_webhook_secret,
		       COALESCE(last_sync_sha, ''),
		       last_sync_at,
		       COALESCE(last_sync_error, ''),
		       created_at, updated_at
		  FROM stack_git_sources WHERE stack_name = ?`, stackName)
	src, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return src, err
}

// Configure is the upsert — create on first call, update after.
func (s *Service) Configure(ctx context.Context, stackName string, in Input) (*Source, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	if err := stacks.ValidateName(stackName); err != nil {
		return nil, ErrStackNameInvalid
	}
	existing, err := s.Get(ctx, stackName)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	branch := in.Branch
	if branch == "" {
		branch = "main"
	}
	path := in.PathInRepo
	if path == "" {
		path = "."
	}
	authKind := in.AuthKind
	if authKind == "" {
		authKind = "none"
	}
	poll := in.PollIntervalSec
	if poll == 0 {
		poll = 300
	}

	// Encrypt-on-update helpers: pick the incoming value, the cleared
	// state (NULL), or keep existing.
	encrypt := func(plain string) ([]byte, error) {
		if plain == "" {
			return nil, nil
		}
		return s.secrets.Encrypt([]byte(plain))
	}

	var pwEnc, sshEnc []byte
	switch {
	case in.ClearPassword:
		pwEnc = nil
	case in.Password != "":
		if enc, err := encrypt(in.Password); err != nil {
			return nil, fmt.Errorf("encrypt password: %w", err)
		} else {
			pwEnc = enc
		}
	}
	switch {
	case in.ClearSSHKey:
		sshEnc = nil
	case in.SSHKey != "":
		if enc, err := encrypt(in.SSHKey); err != nil {
			return nil, fmt.Errorf("encrypt ssh key: %w", err)
		} else {
			sshEnc = enc
		}
	}

	var webhook any
	switch {
	case in.ClearWebhook:
		webhook = nil
	case in.WebhookSecret != "":
		webhook = in.WebhookSecret
	}

	if existing == nil {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO stack_git_sources
			  (stack_name, repo_url, branch, path_in_repo, auth_kind,
			   username, password_encrypted, ssh_key_encrypted,
			   auto_deploy, poll_interval_sec, webhook_secret)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			stackName, in.RepoURL, branch, path, authKind,
			nullable(in.Username), pwEnc, sshEnc,
			boolInt(in.AutoDeploy), poll, webhook)
		if err != nil {
			return nil, fmt.Errorf("insert git source: %w", err)
		}
		return s.Get(ctx, stackName)
	}

	// Update path: build the SET clause conditionally to honour keep-
	// existing semantics for password / ssh_key / webhook_secret.
	clauses := []string{
		"repo_url = ?", "branch = ?", "path_in_repo = ?", "auth_kind = ?",
		"username = ?", "auto_deploy = ?", "poll_interval_sec = ?",
		"updated_at = CURRENT_TIMESTAMP",
	}
	args := []any{
		in.RepoURL, branch, path, authKind,
		nullable(in.Username), boolInt(in.AutoDeploy), poll,
	}
	if in.ClearPassword || in.Password != "" {
		clauses = append(clauses, "password_encrypted = ?")
		args = append(args, pwEnc)
	}
	if in.ClearSSHKey || in.SSHKey != "" {
		clauses = append(clauses, "ssh_key_encrypted = ?")
		args = append(args, sshEnc)
	}
	if in.ClearWebhook || in.WebhookSecret != "" {
		clauses = append(clauses, "webhook_secret = ?")
		args = append(args, webhook)
	}
	args = append(args, stackName)
	q := "UPDATE stack_git_sources SET " + strings.Join(clauses, ", ") + " WHERE stack_name = ?"
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("update git source: %w", err)
	}
	return s.Get(ctx, stackName)
}

func (s *Service) Delete(ctx context.Context, stackName string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stack_git_sources WHERE stack_name = ?`, stackName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Best-effort: remove the clone so a future re-configure with a
	// different repo URL doesn't fight a stale .git dir.
	_ = os.RemoveAll(s.cloneDir(stackName))
	return nil
}

// WebhookSecret is what the webhook handler needs to verify an incoming
// signature — not exposed through the normal Get() to keep it out of
// list responses.
func (s *Service) WebhookSecret(ctx context.Context, stackName string) (string, error) {
	var secret sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT webhook_secret FROM stack_git_sources WHERE stack_name = ?`,
		stackName).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !secret.Valid {
		return "", nil
	}
	return secret.String, nil
}

// -----------------------------------------------------------------------------
// Sync — clone/pull + copy into the stack's FS location
// -----------------------------------------------------------------------------

func (s *Service) Sync(ctx context.Context, stackName string) (*SyncResult, error) {
	src, err := s.Get(ctx, stackName)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res := &SyncResult{OldSHA: src.LastSyncSHA}

	auth, err := s.buildAuth(ctx, src)
	if err != nil {
		s.recordSyncError(ctx, stackName, err)
		return nil, err
	}

	cloneDir := s.cloneDir(stackName)
	newSHA, err := s.cloneOrPull(ctx, src, cloneDir, auth)
	if err != nil {
		s.recordSyncError(ctx, stackName, err)
		return nil, err
	}
	res.NewSHA = newSHA
	res.Changed = newSHA != src.LastSyncSHA

	// Always copy on first sync (when LastSyncSHA is empty) so the
	// stack's FS gets populated, even if the repo already matches.
	if res.Changed || src.LastSyncSHA == "" {
		if err := s.copyIntoStack(cloneDir, src.PathInRepo, stackName); err != nil {
			s.recordSyncError(ctx, stackName, err)
			return nil, err
		}
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE stack_git_sources
		   SET last_sync_sha = ?, last_sync_at = CURRENT_TIMESTAMP,
		       last_sync_error = NULL, updated_at = CURRENT_TIMESTAMP
		 WHERE stack_name = ?`, newSHA, stackName)
	if err != nil {
		return nil, fmt.Errorf("record sync success: %w", err)
	}

	// Auto-deploy hook: only runs when the SHA actually changed AND
	// the source opted in — avoids re-deploying on every poll.
	if res.Changed && src.AutoDeploy && s.deploy != nil {
		deployRes, derr := s.deploy(ctx, stackName)
		if derr != nil {
			slog.Warn("git auto-deploy failed", "stack", stackName, "err", derr)
		} else {
			res.Deployed = true
			res.DeployResult = deployRes
		}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

func (s *Service) recordSyncError(ctx context.Context, stackName string, err error) {
	_, _ = s.db.ExecContext(ctx, `
		UPDATE stack_git_sources
		   SET last_sync_at = CURRENT_TIMESTAMP,
		       last_sync_error = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE stack_name = ?`, err.Error(), stackName)
}

func (s *Service) cloneOrPull(ctx context.Context, src *Source, dir string, auth transportAuth) (string, error) {
	authMethod, err := auth.resolve()
	if err != nil {
		return "", fmt.Errorf("build auth: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		// Existing clone — fetch + reset to remote branch.
		repo, err := git.PlainOpen(dir)
		if err != nil {
			return "", fmt.Errorf("open clone: %w", err)
		}
		// Ensure the remote URL still matches — user could have
		// reconfigured to a different repo.
		if err := resetRemote(repo, src.RepoURL); err != nil {
			return "", err
		}
		fetchOpts := &git.FetchOptions{
			RemoteName: "origin",
			Force:      true,
			Auth:       authMethod,
		}
		if err := repo.FetchContext(ctx, fetchOpts); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return "", fmt.Errorf("fetch: %w", err)
		}
		branchRef := plumbing.NewRemoteReferenceName("origin", src.Branch)
		ref, err := repo.Reference(branchRef, true)
		if err != nil {
			return "", fmt.Errorf("resolve branch %q: %w", src.Branch, err)
		}
		wt, err := repo.Worktree()
		if err != nil {
			return "", err
		}
		if err := wt.Checkout(&git.CheckoutOptions{Hash: ref.Hash(), Force: true}); err != nil {
			return "", fmt.Errorf("checkout: %w", err)
		}
		return ref.Hash().String(), nil
	}
	// Fresh clone.
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	repo, err := git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           src.RepoURL,
		ReferenceName: plumbing.NewBranchReferenceName(src.Branch),
		SingleBranch:  true,
		Depth:         1,
		Auth:          authMethod,
	})
	if err != nil {
		return "", fmt.Errorf("clone: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}

// copyIntoStack reads compose.yaml (+ optional .env) from the clone's
// path_in_repo and pushes them through stacks.Manager so fsnotify fires
// and the normal deploy path sees the change.
//
// When path_in_repo points at a directory, ALL sibling files in that
// directory are also mirrored into the stack dir so compose `build:`
// references (Dockerfiles, config files, etc.) resolve correctly.
// Files prefixed with . are skipped except .env / .env.age (the
// canonical secrets). Fixes FINDING-8.
func (s *Service) copyIntoStack(cloneDir, pathInRepo, stackName string) error {
	src := filepath.Join(cloneDir, pathInRepo)
	composePath := src
	isDir := false
	if info, err := os.Stat(src); err == nil && info.IsDir() {
		isDir = true
		for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
			p := filepath.Join(src, name)
			if _, err := os.Stat(p); err == nil {
				composePath = p
				break
			}
		}
	}
	compose, err := os.ReadFile(composePath)
	if err != nil {
		return ErrComposeMissing
	}
	envContent := ""
	envPath := filepath.Join(filepath.Dir(composePath), ".env")
	if b, err := os.ReadFile(envPath); err == nil {
		envContent = string(b)
	}
	// Create/update the stack with compose + .env first; the Manager
	// wires up the stack directory we'll then copy siblings into.
	if _, err := s.stacks.Get(stackName); err != nil {
		if _, err := s.stacks.Create(stackName, string(compose), envContent); err != nil {
			return fmt.Errorf("create stack from git: %w", err)
		}
	} else {
		if _, err := s.stacks.Update(stackName, string(compose), envContent); err != nil {
			return fmt.Errorf("update stack from git: %w", err)
		}
	}
	// Mirror sibling files when path_in_repo was a directory.
	if !isDir {
		return nil
	}
	dstDir, err := s.stacks.Dir(stackName)
	if err != nil {
		return nil // non-fatal — compose + env are already in place
	}
	return copyTreeSiblings(filepath.Dir(composePath), dstDir)
}

// copyTreeSiblings mirrors all non-dotfile entries under src into dst,
// recursively, EXCEPT the compose / .env files the main copy already
// handled. Tops out at 64 MiB per file to keep build-contexts sane.
func copyTreeSiblings(src, dst string) error {
	const maxFile = 64 << 20
	skip := map[string]bool{
		"compose.yaml":        true,
		"compose.yml":         true,
		"docker-compose.yaml": true,
		"docker-compose.yml":  true,
		".env":                true,
		".git":                true,
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		// skip top-level compose / .env (already written) and anything
		// git-adjacent.
		if skip[rel] || strings.HasPrefix(rel, ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dstPath := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxFile {
			return fmt.Errorf("file %s exceeds %d bytes — refusing to copy into stack dir", rel, maxFile)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, b, info.Mode().Perm())
	})
}

// -----------------------------------------------------------------------------
// Polling loop
// -----------------------------------------------------------------------------

func (s *Service) pollLoop(ctx context.Context) {
	defer s.wg.Done()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-tick.C:
			s.pollDue(ctx)
		}
	}
}

func (s *Service) pollDue(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, poll_interval_sec, last_sync_at
		  FROM stack_git_sources
		 WHERE poll_interval_sec > 0`)
	if err != nil {
		slog.Warn("gitsource poll list", "err", err)
		return
	}
	defer rows.Close()
	now := time.Now()
	var due []string
	for rows.Next() {
		var name string
		var interval int
		var last sql.NullTime
		if err := rows.Scan(&name, &interval, &last); err != nil {
			continue
		}
		if !last.Valid || now.Sub(last.Time) >= time.Duration(interval)*time.Second {
			due = append(due, name)
		}
	}
	for _, name := range due {
		if _, err := s.Sync(ctx, name); err != nil {
			slog.Warn("gitsource poll sync", "stack", name, "err", err)
		}
	}
}

// -----------------------------------------------------------------------------
// Auth plumbing
// -----------------------------------------------------------------------------

// transportAuth builds a git transport.AuthMethod lazily (SSH needs
// key parsing that can fail). Returned nil = unauthenticated.
type transportAuth func() (transport.AuthMethod, error)

func (s *Service) buildAuth(ctx context.Context, src *Source) (transportAuth, error) {
	switch src.AuthKind {
	case "", "none":
		return func() (transport.AuthMethod, error) { return nil, nil }, nil
	case "http":
		pw, err := s.decryptField(ctx, src.StackName, "password_encrypted")
		if err != nil {
			return nil, err
		}
		user := src.Username
		return func() (transport.AuthMethod, error) {
			return &httpauth.BasicAuth{Username: user, Password: pw}, nil
		}, nil
	case "ssh":
		key, err := s.decryptField(ctx, src.StackName, "ssh_key_encrypted")
		if err != nil {
			return nil, err
		}
		user := src.Username
		if user == "" {
			user = "git"
		}
		pemKey := []byte(key)
		return func() (transport.AuthMethod, error) {
			return sshauth.NewPublicKeys(user, pemKey, "")
		}, nil
	default:
		return nil, ErrInvalidAuthKind
	}
}

// resolve returns the auth method or nil (for no-auth) — the caller
// forwards nil to go-git which treats it as anonymous.
func (a transportAuth) resolve() (transport.AuthMethod, error) {
	if a == nil {
		return nil, nil
	}
	return a()
}

// decryptField pulls the encrypted column value and decrypts it.
// Returns "" (with no error) if the column is NULL.
func (s *Service) decryptField(ctx context.Context, stackName, col string) (string, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT `+col+` FROM stack_git_sources WHERE stack_name = ?`, stackName).
		Scan(&raw)
	if err != nil {
		return "", err
	}
	if raw == nil {
		return "", nil
	}
	plain, err := s.secrets.Decrypt(raw)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", col, err)
	}
	return string(plain), nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func (s *Service) cloneDir(stackName string) string {
	return filepath.Join(s.cacheDir, stackName)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(r rowScanner) (*Source, error) {
	var src Source
	var username, lastSHA, lastErr string
	var hasPassword, hasSSHKey, hasWebhook, autoDeploy int
	var lastAt sql.NullTime
	if err := r.Scan(&src.StackName, &src.RepoURL, &src.Branch, &src.PathInRepo, &src.AuthKind,
		&username, &hasPassword, &hasSSHKey,
		&autoDeploy, &src.PollIntervalSec,
		&hasWebhook, &lastSHA, &lastAt, &lastErr,
		&src.CreatedAt, &src.UpdatedAt); err != nil {
		return nil, err
	}
	src.Username = username
	src.HasPassword = hasPassword == 1
	src.HasSSHKey = hasSSHKey == 1
	src.HasWebhookSecret = hasWebhook == 1
	src.AutoDeploy = autoDeploy == 1
	src.LastSyncSHA = lastSHA
	src.LastSyncError = lastErr
	if lastAt.Valid {
		t := lastAt.Time
		src.LastSyncAt = &t
	}
	return &src, nil
}

func validateInput(in Input) error {
	if strings.TrimSpace(in.RepoURL) == "" {
		return errors.New("repo_url is required")
	}
	switch in.AuthKind {
	case "", "none", "http", "ssh":
	default:
		return ErrInvalidAuthKind
	}
	if in.PollIntervalSec != 0 && in.PollIntervalSec < 60 {
		return ErrPollTooShort
	}
	return nil
}

func safeStackName(s string) bool {
	if s == "" || strings.ContainsAny(s, `/\:*?"<>|`) {
		return false
	}
	return true
}

func resetRemote(repo *git.Repository, url string) error {
	rem, err := repo.Remote("origin")
	if err != nil {
		return repo.DeleteRemote("origin")
	}
	if len(rem.Config().URLs) > 0 && rem.Config().URLs[0] == url {
		return nil
	}
	if err := repo.DeleteRemote("origin"); err != nil {
		return err
	}
	_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// deployResultJSON keeps a DeployResult JSON-serialisable when callers
// want to pass it back through an HTTP response without importing the
// compose package. Not used directly here — left as a helper for
// handlers that only have a Service reference.
func deployResultJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
