// Package gitsource keeps a stack's compose.yaml + .env in sync with a
// git repository.
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
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/safepath"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacklock"
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
	ErrPollTooShort     = errors.New("poll_interval_sec must be 0 (no polling) or >= 60")
	ErrComposeMissing   = errors.New("repo did not contain compose.yaml at path_in_repo")
	ErrStackNameInvalid = errors.New("stack name contains characters that cannot be used on disk")
	ErrPathOutsideRepo  = errors.New("path_in_repo must be a relative path inside the repository")
)

var ErrWebhookSecretShort = fmt.Errorf("webhook_secret must be at least %d characters", minWebhookSecretLen)

// Source is the DB-backed row, returned to handlers with secrets
// redacted via the Has* flags instead of raw values.
type Source struct {
	StackName        string         `json:"stack_name"`
	RepoURL          string         `json:"repo_url"`
	Branch           string         `json:"branch"`
	PathInRepo       string         `json:"path_in_repo"`
	AuthKind         string         `json:"auth_kind"`
	Username         string         `json:"username,omitempty"`
	HasPassword      bool           `json:"has_password"`
	HasSSHKey        bool           `json:"has_ssh_key"`
	AutoDeploy       bool           `json:"auto_deploy"`
	PollIntervalSec  int            `json:"poll_interval_sec"`
	HasWebhookSecret bool           `json:"has_webhook_secret"`
	LastSyncSHA      string         `json:"last_sync_sha,omitempty"`
	LastSyncAt       *time.Time     `json:"last_sync_at,omitempty"`
	LastSyncError    string         `json:"last_sync_error,omitempty"`
	LastEnvDrift     *EnvDrift      `json:"last_env_drift,omitempty"`
	PendingDeploy    *PendingDeploy `json:"pending_deploy,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// PendingDeploy is a synced commit whose auto-deploy found the stack busy.
// It deploys as soon as the stack is free.
type PendingDeploy struct {
	SHA       string    `json:"sha"`
	Operation string    `json:"operation"`
	Since     time.Time `json:"since"`
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
	PollIntervalSec *int   `json:"poll_interval_sec,omitempty"`
	WebhookSecret   string `json:"webhook_secret,omitempty"`
	ClearWebhook    bool   `json:"clear_webhook_secret,omitempty"`
}

// SyncResult records what a sync did — used by handlers and by the
// polling goroutine's auto-deploy decision.
type SyncResult struct {
	OldSHA       string    `json:"old_sha,omitempty"`
	NewSHA       string    `json:"new_sha"`
	Changed      bool      `json:"changed"`
	Deployed     bool      `json:"deployed,omitempty"`
	DeployResult any       `json:"deploy_result,omitempty"`
	EnvDrift     *EnvDrift `json:"env_drift,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
}

// DeployFunc is the callback the service invokes when auto_deploy is on
// and the sync pulled a new SHA.
type DeployFunc func(ctx context.Context, stackName string) (any, error)

type Service struct {
	db       *sql.DB
	secrets  *secrets.Service
	stacks   *stacks.Manager
	cacheDir string
	deploy   DeployFunc
	// deployNow deploys a pending commit without waiting for the stack.
	deployNow DeployFunc

	stop chan struct{}
	wg   sync.WaitGroup
	// runCtx bounds pending deploys started from lock releases; Stop
	// cancels it and waits for them.
	runCtx    context.Context
	cancelRun context.CancelFunc
	stopped   bool // under syncMu

	syncMu    sync.Mutex
	stackMu   map[string]*sync.Mutex
	triggered map[string]bool // stack -> another sync was requested while one ran

	// syncFn replaces the clone/copy/deploy body in tests.
	syncFn func(ctx context.Context, stackName string) (*SyncResult, error)
}

func New(db *sql.DB, secretsSvc *secrets.Service, stacksMgr *stacks.Manager, cacheDir string, deploy DeployFunc) *Service {
	runCtx, cancelRun := context.WithCancel(context.Background())
	return &Service{
		runCtx:    runCtx,
		cancelRun: cancelRun,
		db:        db,
		secrets:   secretsSvc,
		stacks:    stacksMgr,
		cacheDir:  cacheDir,
		deploy:    deploy,
		stop:      make(chan struct{}),
		stackMu:   make(map[string]*sync.Mutex),
		triggered: make(map[string]bool),
	}
}

// Start launches the polling goroutine. Safe to call without a deploy
// callback — the service just becomes a manual-sync-only surface.
func (s *Service) Start(ctx context.Context) {
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		slog.Warn("gitsource cache dir", "err", err, "path", s.cacheDir)
	}
	if n, err := s.EncryptLegacyWebhookSecrets(ctx); err != nil {
		slog.Warn("gitsource: encrypt stored webhook secrets", "err", err)
	} else if n > 0 {
		slog.Info("gitsource: encrypted stored webhook secrets", "count", n)
	}
	s.wg.Add(1)
	go s.pollLoop(ctx)
	s.goPending(func(runCtx context.Context) { s.deployAllPending(runCtx) })
}

func (s *Service) Stop() {
	s.syncMu.Lock()
	s.stopped = true
	s.syncMu.Unlock()
	s.cancelRun()
	close(s.stop)
	s.wg.Wait()
}

// goPending runs fn in a goroutine that Stop cancels and waits for.
func (s *Service) goPending(fn func(ctx context.Context)) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.stopped {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(s.runCtx)
	}()
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
		       COALESCE(webhook_secret, '') <> '' AS has_webhook_secret,
		       COALESCE(last_sync_sha, ''),
		       last_sync_at,
		       COALESCE(last_sync_error, ''),
		       COALESCE(last_env_drift, ''),
		       COALESCE(pending_sha, ''), COALESCE(pending_operation, ''), pending_since,
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
	// Absent keeps the stored interval (300 s for a new source); 0 means
	// webhook or manual sync only.
	poll := defaultPollSec
	switch {
	case in.PollIntervalSec != nil:
		poll = *in.PollIntervalSec
	case existing != nil:
		poll = existing.PollIntervalSec
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
		enc, err := s.encryptWebhookSecret(in.WebhookSecret)
		if err != nil {
			return nil, fmt.Errorf("encrypt webhook secret: %w", err)
		}
		webhook = enc
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
	if !in.AutoDeploy {
		clauses = append(clauses, "pending_sha = NULL", "pending_operation = NULL", "pending_since = NULL")
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
	return s.decryptWebhookSecret(secret.String)
}

// -----------------------------------------------------------------------------
// Sync — clone/pull + copy into the stack's FS location
// -----------------------------------------------------------------------------

// webhookSyncTimeout bounds a webhook-triggered sync, which outlives the
// request that queued it.
const webhookSyncTimeout = 10 * time.Minute

// Sync pulls the stack's repo and copies it into the stack. Syncs of the
// same stack (poller, manual, webhook) run one at a time, since they share
// the clone dir and the stack files.
func (s *Service) Sync(ctx context.Context, stackName string) (*SyncResult, error) {
	mu := s.stackLock(stackName)
	mu.Lock()
	defer mu.Unlock()
	if s.syncFn != nil {
		return s.syncFn(ctx, stackName)
	}
	return s.sync(ctx, stackName, false)
}

// ErrStackAppeared ends a first sync when a stack of that name showed up
// while the repo was being cloned; a first sync only ever creates.
var ErrStackAppeared = errors.New("a stack of this name appeared during the first sync")

// stackCreatedError marks a sync that created the stack before failing.
type stackCreatedError struct{ error }

func (e stackCreatedError) Unwrap() error { return e.error }

// FirstSync runs the initial sync of a source that was just configured and
// calls undo if it fails, telling it whether this sync created the stack.
// Holding the stack's lock across both keeps a queued poller or webhook
// sync from working on a source being removed. A source that has synced
// successfully in the meantime is left alone.
func (s *Service) FirstSync(ctx context.Context, stackName string, undo func(created bool)) (*SyncResult, error) {
	mu := s.stackLock(stackName)
	mu.Lock()
	defer mu.Unlock()
	var (
		res *SyncResult
		err error
	)
	if s.syncFn != nil {
		res, err = s.syncFn(ctx, stackName)
	} else {
		res, err = s.sync(ctx, stackName, true)
	}
	if err == nil {
		return res, nil
	}
	if src, gerr := s.Get(context.WithoutCancel(ctx), stackName); gerr == nil && src.LastSyncSHA != "" {
		return nil, err
	}
	var created stackCreatedError
	undo(errors.As(err, &created))
	return nil, err
}

func (s *Service) stackLock(stackName string) *sync.Mutex {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	mu, ok := s.stackMu[stackName]
	if !ok {
		mu = &sync.Mutex{}
		s.stackMu[stackName] = mu
	}
	return mu
}

// TriggerSync starts a background sync for a webhook delivery and returns
// at once. Deliveries arriving while one runs collapse into a single
// follow-up sync, so replaying a delivery can't pile up syncs or deploys.
func (s *Service) TriggerSync(ctx context.Context, stackName string) {
	s.syncMu.Lock()
	if _, running := s.triggered[stackName]; running {
		s.triggered[stackName] = true
		s.syncMu.Unlock()
		return
	}
	s.triggered[stackName] = false
	s.syncMu.Unlock()

	base := context.WithoutCancel(ctx)
	go func() {
		for {
			runCtx, cancel := context.WithTimeout(base, webhookSyncTimeout)
			if _, err := s.Sync(runCtx, stackName); err != nil {
				slog.Warn("git webhook sync failed", "stack", stackName, "err", err)
			}
			cancel()

			s.syncMu.Lock()
			again := s.triggered[stackName]
			if !again {
				delete(s.triggered, stackName)
				s.syncMu.Unlock()
				return
			}
			s.triggered[stackName] = false
			s.syncMu.Unlock()
		}
	}()
}

func (s *Service) sync(ctx context.Context, stackName string, firstSync bool) (*SyncResult, error) {
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

	created := false
	var copiedAt time.Time
	// Always copy on first sync (when LastSyncSHA is empty) so the
	// stack's FS gets populated, even if the repo already matches.
	if res.Changed || src.LastSyncSHA == "" {
		// Before copying: the copy merges the stack's current .env and
		// records the result as written by Dockmesh, which would hide
		// changes made outside it from the deploy check.
		if s.stacks != nil {
			if _, gerr := s.stacks.Get(stackName); gerr == nil {
				if err := s.stacks.CheckGitDeploy(ctx, stackName); err != nil {
					s.recordSyncError(ctx, stackName, err)
					return nil, err
				}
			}
		}
		drift, justCreated, err := s.copyIntoStack(cloneDir, src.PathInRepo, stackName, firstSync)
		copiedAt = time.Now()
		created = justCreated
		if err != nil {
			s.recordSyncError(ctx, stackName, err)
			return nil, syncFailure(err, created)
		}
		if len(drift.NewFromRepo)+len(drift.NewFromCompose)+len(drift.UserOnly) > 0 {
			res.EnvDrift = &drift
		}
	}

	var driftJSON sql.NullString
	if res.EnvDrift != nil {
		if b, err := json.Marshal(res.EnvDrift); err == nil {
			driftJSON = sql.NullString{String: string(b), Valid: true}
		}
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE stack_git_sources
		   SET last_sync_sha = ?, last_sync_at = CURRENT_TIMESTAMP,
		       last_sync_error = NULL, last_env_drift = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE stack_name = ?`, newSHA, driftJSON, stackName)
	if err != nil {
		return nil, syncFailure(fmt.Errorf("record sync success: %w", err), created)
	}

	// Auto-deploy hook: only runs when the SHA actually changed AND
	// the source opted in — avoids re-deploying on every poll.
	switch {
	case res.Changed && src.AutoDeploy && s.deploy != nil:
		deployRes, derr := s.deploy(ctx, stackName)
		var busy *stacklock.BusyError
		switch {
		case errors.As(derr, &busy) && deploysFiles(busy.Operation) && busy.Since.After(copiedAt):
			slog.Info("git auto-deploy left to the running deploy", "stack", stackName, "sha", newSHA, "operation", busy.Operation)
		case errors.As(derr, &busy):
			s.setPending(ctx, stackName, newSHA, busy.Operation)
			slog.Info("git auto-deploy pending: stack busy", "stack", stackName, "sha", newSHA, "operation", busy.Operation)
			// The stack may have become free before the commit was stored.
			s.goPending(func(runCtx context.Context) { s.DeployPending(runCtx, stackName) })
		case derr != nil:
			slog.Warn("git auto-deploy failed", "stack", stackName, "err", derr)
		default:
			res.Deployed = true
			res.DeployResult = deployRes
		}
	case src.PendingDeploy != nil:
		s.deployPendingLocked(ctx, src)
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// deploysFiles reports whether op deploys the stack's current files. Such an
// operation that started after the sync copied a commit deploys that commit
// itself, or a rollback the user chose over it.
func deploysFiles(op string) bool {
	switch op {
	case stacklock.OpDeploy, stacklock.OpDependency, stacklock.OpGitDeploy:
		return true
	}
	return false
}

// SetDeployNow sets the callback that deploys a pending commit. It must not
// wait for a busy stack but return its *stacklock.BusyError, and it clears
// the pending commit once it holds the stack.
func (s *Service) SetDeployNow(fn DeployFunc) { s.deployNow = fn }

// StackReleased deploys the stack's pending commit, if any, now that its
// lock was released; an empty stack name stands for every stack.
func (s *Service) StackReleased(stackName string) {
	s.goPending(func(ctx context.Context) { s.stackReleased(ctx, stackName) })
}

func (s *Service) stackReleased(ctx context.Context, stackName string) {
	if stackName == "" {
		s.deployAllPending(ctx)
		return
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stack_git_sources WHERE stack_name = ? AND pending_sha IS NOT NULL`, stackName).Scan(&n)
	if err != nil {
		slog.Warn("gitsource: read pending deploy", "stack", stackName, "err", err)
		return
	}
	if n > 0 {
		s.DeployPending(ctx, stackName)
	}
}

// DeployPending deploys the stack's pending commit unless the stack is
// still busy. A failed deploy is reported like any other and drops the
// pending commit, so it is not retried on every lock release.
func (s *Service) DeployPending(ctx context.Context, stackName string) {
	ctx, cancel := context.WithTimeout(ctx, webhookSyncTimeout)
	defer cancel()
	mu := s.stackLock(stackName)
	mu.Lock()
	defer mu.Unlock()
	src, err := s.Get(ctx, stackName)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			slog.Warn("gitsource: read pending deploy", "stack", stackName, "err", err)
		}
		return
	}
	if src.PendingDeploy != nil {
		s.deployPendingLocked(ctx, src)
	}
}

func (s *Service) deployPendingLocked(ctx context.Context, src *Source) {
	if !src.AutoDeploy || s.deployNow == nil {
		_ = s.ClearPending(ctx, src.StackName)
		return
	}
	_, err := s.deployNow(ctx, src.StackName)
	var busy *stacklock.BusyError
	switch {
	case errors.As(err, &busy):
		s.setPending(ctx, src.StackName, src.PendingDeploy.SHA, busy.Operation)
	case err != nil:
		_ = s.ClearPending(ctx, src.StackName)
		slog.Warn("git pending deploy failed", "stack", src.StackName, "sha", src.PendingDeploy.SHA, "err", err)
	}
}

func (s *Service) deployAllPending(ctx context.Context) {
	pending, err := s.PendingDeploys(ctx)
	if err != nil {
		slog.Warn("gitsource: list pending deploys", "err", err)
		return
	}
	for name := range pending {
		s.DeployPending(ctx, name)
	}
}

// PendingDeploys returns the pending commit of every stack that has one.
func (s *Service) PendingDeploys(ctx context.Context) (map[string]*PendingDeploy, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, pending_sha, COALESCE(pending_operation, ''), pending_since
		  FROM stack_git_sources WHERE pending_sha IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*PendingDeploy{}
	for rows.Next() {
		var name string
		var p PendingDeploy
		var since sql.NullTime
		if err := rows.Scan(&name, &p.SHA, &p.Operation, &since); err != nil {
			return nil, err
		}
		p.Since = since.Time
		out[name] = &p
	}
	return out, rows.Err()
}

// setPending stores sha as the stack's pending commit. A newer commit
// replaces an older one; the time it has waited is kept.
func (s *Service) setPending(ctx context.Context, stackName, sha, operation string) {
	_, err := s.db.ExecContext(context.WithoutCancel(ctx), `
		UPDATE stack_git_sources
		   SET pending_sha = ?, pending_operation = ?,
		       pending_since = COALESCE(pending_since, CURRENT_TIMESTAMP)
		 WHERE stack_name = ?`, sha, operation, stackName)
	if err != nil {
		slog.Warn("gitsource: store pending deploy", "stack", stackName, "err", err)
	}
}

// ClearPending drops the stack's pending commit: whoever calls it is about
// to deploy the stack's current files.
func (s *Service) ClearPending(ctx context.Context, stackName string) error {
	_, err := s.db.ExecContext(context.WithoutCancel(ctx), `
		UPDATE stack_git_sources
		   SET pending_sha = NULL, pending_operation = NULL, pending_since = NULL
		 WHERE stack_name = ? AND pending_sha IS NOT NULL`, stackName)
	if err != nil {
		slog.Warn("gitsource: clear pending deploy", "stack", stackName, "err", err)
	}
	return err
}

func syncFailure(err error, created bool) error {
	if created {
		return stackCreatedError{err}
	}
	return err
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
// references (Dockerfiles, config files, etc.) resolve correctly;
// skipSibling lists what is never mirrored.
//
// .env policy: on first sync (no existing stack) seed from the repo's
// .env. On every subsequent sync, run mergeEnvFiles so the operator's
// filled-in values survive but new repo keys + new compose ${VAR}
// references show up at the end of the file (with comments). The
// caller persists the returned EnvDrift in stack_git_sources so the
// UI can render a "drift detected" banner.
func (s *Service) copyIntoStack(cloneDir, pathInRepo, stackName string, firstSync bool) (drift EnvDrift, created bool, err error) {
	// The repo decides what its files are, including symlinks, so the clone
	// is only read through an os.Root: nothing outside it can be reached.
	repo, err := os.OpenRoot(cloneDir)
	if err != nil {
		return drift, created, err
	}
	defer repo.Close()
	src, err := readCompose(repo, pathInRepo)
	if err != nil {
		return drift, created, err
	}
	compose, envFromRepo := src.compose, src.env
	if _, err := s.stacks.Get(stackName); err != nil {
		// First sync — seed both compose + .env straight from the repo.
		// The merger still runs to surface compose `${VAR}` that the
		// repo's .env doesn't cover (catches drift in fresh repos too).
		seeded, d := mergeEnvFiles(envFromRepo, envFromRepo, string(compose))
		drift = d
		if _, err := s.stacks.Create(stackName, string(compose), seeded); err != nil {
			return drift, created, fmt.Errorf("create stack from git: %w", err)
		}
		created = true
	} else {
		if firstSync {
			return drift, created, ErrStackAppeared
		}
		// Read the stack's CURRENT env through the stacks.Manager so
		// the merger sees the decrypted content. Reading .env directly
		// from disk misses the case where secrets-encryption is on
		// and the file is .env.age — in that case the merger would
		// see an empty stack-env and incorrectly mark every key from
		// the repo as "new from repo" drift on every sync.
		stackEnv := ""
		if detail, derr := s.stacks.Get(stackName); derr == nil {
			stackEnv = detail.Env
		}
		merged, d := mergeEnvFiles(stackEnv, envFromRepo, string(compose))
		drift = d
		if _, err := s.stacks.Update(stackName, string(compose), merged); err != nil {
			return drift, created, fmt.Errorf("update stack from git: %w", err)
		}
	}
	// Mirror sibling files when path_in_repo was a directory.
	if !src.isDir {
		return drift, created, nil
	}
	dstDir, err := s.stacks.Dir(stackName)
	if err != nil {
		return drift, created, nil // non-fatal — compose + env are already in place
	}
	return drift, created, copyTreeSiblings(repo, src.dir, dstDir)
}

type repoCompose struct {
	compose, env string
	dir          string // directory of the compose file, relative to the clone
	isDir        bool   // path_in_repo named a directory
}

// readCompose finds the compose file (and an optional .env next to it) at
// pathInRepo inside the clone.
func readCompose(repo *os.Root, pathInRepo string) (repoCompose, error) {
	var out repoCompose
	rel := filepath.Clean(filepath.FromSlash(pathInRepo))
	composeRel := rel
	info, err := repo.Stat(rel)
	if safepath.IsRootEscape(err) {
		return out, ErrPathOutsideRepo
	}
	if err == nil && info.IsDir() {
		out.isDir = true
		for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
			p := filepath.Join(rel, name)
			_, err := repo.Stat(p)
			if safepath.IsRootEscape(err) {
				return out, ErrPathOutsideRepo
			}
			if err == nil {
				composeRel = p
				break
			}
		}
	}
	b, err := repo.ReadFile(composeRel)
	if safepath.IsRootEscape(err) {
		return out, ErrPathOutsideRepo
	}
	if err != nil {
		return out, ErrComposeMissing
	}
	out.compose = string(b)
	out.dir = filepath.Dir(composeRel)
	if env, err := repo.ReadFile(filepath.Join(out.dir, ".env")); err == nil {
		out.env = string(env)
	}
	return out, nil
}

// skipSibling reports whether a repo path must not be mirrored into the
// stack dir: the compose/.env files the main copy writes, git metadata, and
// files Dockmesh owns there. The meta file carries migration hooks run via
// sh -c and the default override; override files change what a deploy
// runs. A repo push must not be able to replace either.
func skipSibling(rel string) bool {
	// Lower-cased: the stack dir may be on a case-insensitive filesystem.
	rel = strings.ToLower(rel)
	if strings.HasPrefix(rel, ".git") {
		return true
	}
	if strings.Contains(rel, "/") {
		return false
	}
	switch rel {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
		".env", ".env.age":
		return true
	}
	return strings.HasPrefix(rel, ".dockmesh") || compose.IsEnvironmentFile(rel)
}

// copyTreeSiblings mirrors the entries under srcRel (relative to the
// clone) into dst, recursively, except what skipSibling excludes. Tops out at 64 MiB per file to keep
// build-contexts sane. Symlinks are copied as the file they point to and
// skipped when that lies outside the clone. dst is written through an
// os.Root as well: containers bind-mount parts of the stack directory and
// could leave symlinks there.
func copyTreeSiblings(repo *os.Root, srcRel, dst string) error {
	const maxFile = 64 << 20
	out, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	fsys := repo.FS()
	start := filepath.ToSlash(srcRel)
	return fs.WalkDir(fsys, start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, start), "/")
		if start == "." {
			rel = p
		}
		if rel == "" || rel == "." {
			return nil
		}
		if skipSibling(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dstRel := filepath.FromSlash(rel)
		if d.IsDir() {
			return out.MkdirAll(dstRel, 0o755)
		}
		info, err := fs.Stat(fsys, p)
		if safepath.IsRootEscape(err) {
			slog.Warn("git sync: skipping symlink that leaves the repository", "path", rel)
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > maxFile {
			return fmt.Errorf("file %s exceeds %d bytes — refusing to copy into stack dir", rel, maxFile)
		}
		if err := out.MkdirAll(filepath.Dir(dstRel), 0o755); err != nil {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return out.WriteFile(dstRel, b, info.Mode().Perm())
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
	var username, lastSHA, lastErr, lastDriftJSON, pendingSHA, pendingOp string
	var hasPassword, hasSSHKey, hasWebhook, autoDeploy int
	var lastAt, pendingSince sql.NullTime
	if err := r.Scan(&src.StackName, &src.RepoURL, &src.Branch, &src.PathInRepo, &src.AuthKind,
		&username, &hasPassword, &hasSSHKey,
		&autoDeploy, &src.PollIntervalSec,
		&hasWebhook, &lastSHA, &lastAt, &lastErr, &lastDriftJSON,
		&pendingSHA, &pendingOp, &pendingSince,
		&src.CreatedAt, &src.UpdatedAt); err != nil {
		return nil, err
	}
	if pendingSHA != "" {
		src.PendingDeploy = &PendingDeploy{SHA: pendingSHA, Operation: pendingOp, Since: pendingSince.Time}
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
	if lastDriftJSON != "" {
		var d EnvDrift
		if err := json.Unmarshal([]byte(lastDriftJSON), &d); err == nil {
			if len(d.NewFromRepo)+len(d.NewFromCompose)+len(d.UserOnly) > 0 {
				src.LastEnvDrift = &d
			}
		}
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
	if p := in.PollIntervalSec; p != nil && *p != 0 && *p < 60 {
		return ErrPollTooShort
	}
	// Only new secrets are checked; shorter ones stored earlier keep working.
	if in.WebhookSecret != "" && len(in.WebhookSecret) < minWebhookSecretLen {
		return ErrWebhookSecretShort
	}
	p := filepath.ToSlash(in.PathInRepo)
	if strings.HasPrefix(p, "/") || filepath.IsAbs(in.PathInRepo) || slices.Contains(strings.Split(p, "/"), "..") {
		return ErrPathOutsideRepo
	}
	return nil
}

// minWebhookSecretLen keeps webhook secrets out of guessing range.
const minWebhookSecretLen = 16

const defaultPollSec = 300

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
