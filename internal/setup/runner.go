package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/auth"
)

// CommitInput is the wizard's final submit payload — every field the
// operator chose across the seven steps. Validated again on the server
// before the install runs; the live-validate endpoints are advisory.
type CommitInput struct {
	DataDir string `json:"data_dir"`
	// StacksDir is empty for <data dir>/stacks.
	StacksDir   string `json:"stacks_dir,omitempty"`
	ServiceUser struct {
		Mode      string `json:"mode"` // "existing" | "create"
		Username  string `json:"username"`
		AddDocker bool   `json:"add_to_docker_group"`
	} `json:"service_user"`
	Admin struct {
		Username string `json:"username"`
		Email    string `json:"email,omitempty"`
		Password string `json:"password"`
	} `json:"admin"`
	PublicURL string `json:"public_url"`
	// TLS is what a new install serves after setup; empty is the
	// internal certificate on :8080.
	TLS struct {
		Mode     string `json:"mode,omitempty"`
		HTTPAddr string `json:"http_addr,omitempty"`
		CertFile string `json:"cert_file,omitempty"`
		KeyFile  string `json:"key_file,omitempty"`
		// TrustedProxies replaces the default with tls mode proxy.
		TrustedProxies string `json:"trusted_proxies,omitempty"`
	} `json:"tls,omitempty"`
	// Agent is the agent listener ":<port>" and the agent URL, empty for
	// :8443 and the URL derived from PublicURL.
	Agent struct {
		Listen string `json:"listen,omitempty"`
		URL    string `json:"url,omitempty"`
	} `json:"agent,omitempty"`
}

// Validate returns the first user-input issue or nil. Cheaper than the
// per-field validate endpoints which the frontend uses on type — this
// is the gatekeeper the commit handler runs before anything mutates.
func (in *CommitInput) Validate() error {
	if in.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if in.ServiceUser.Username == "" {
		return errors.New("service user username is required")
	}
	if in.ServiceUser.Mode != "existing" && in.ServiceUser.Mode != "create" {
		return errors.New("service_user.mode must be 'existing' or 'create'")
	}
	if !validUsername(in.ServiceUser.Username) {
		return errors.New("invalid service user name")
	}
	if in.Admin.Username == "" {
		return errors.New("admin username is required")
	}
	if utf8.RuneCountInString(in.Admin.Password) < auth.MinPasswordLength {
		return fmt.Errorf("admin password must be at least %d characters", auth.MinPasswordLength)
	}
	if in.PublicURL == "" {
		return errors.New("public_url is required")
	}
	if err := agents.ValidateBaseURL(in.PublicURL); err != nil {
		return fmt.Errorf("public_url %w", err)
	}
	return nil
}

// Event is a single line emitted by the install runner — what the UI
// renders as a row in Step 7's terminal block. Stable JSON shape so the
// SSE consumer can colour-code without inferring from text.
type Event struct {
	TS      time.Time `json:"ts"`
	Step    string    `json:"step"`    // short identifier like "user.create"
	Message string    `json:"message"` // human-readable line
	Status  string    `json:"status"`  // "info" | "ok" | "warn" | "fail" | "done"
}

// Runner drives the install. Multiple subscribers can read from the
// same Run via Subscribe; events are fanned out and buffered so a slow
// reader doesn't block the runner. Once the runner finishes, late
// subscribers still get the full history (Replay).
type Runner struct {
	mu      sync.Mutex
	events  []Event
	subs    []chan Event
	done    bool
	doneErr error
}

// NewRunner creates an empty runner. Caller is responsible for calling
// Start() in a goroutine and serving Subscribe()'d streams.
func NewRunner() *Runner {
	return &Runner{events: make([]Event, 0, 32)}
}

// publish appends an event and fans it out to live subscribers. Safe
// for concurrent calls from the install steps.
func (r *Runner) publish(step, message, status string) {
	ev := Event{TS: time.Now(), Step: step, Message: message, Status: status}
	r.mu.Lock()
	r.events = append(r.events, ev)
	subs := append([]chan Event(nil), r.subs...)
	r.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// Drop on slow consumer — the replay buffer holds the
			// authoritative history.
		}
	}
}

// Subscribe returns a channel of events plus the snapshot of events
// that already happened before the subscribe call. The channel is
// closed when the runner finishes; callers should drain it.
func (r *Runner) Subscribe() (<-chan Event, []Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan Event, 64)
	if r.done {
		// Nothing more to come — return an already-closed channel and
		// the full event history.
		close(ch)
		hist := append([]Event(nil), r.events...)
		return ch, hist
	}
	r.subs = append(r.subs, ch)
	hist := append([]Event(nil), r.events...)
	return ch, hist
}

// finish closes all subscriber channels and marks the runner done.
func (r *Runner) finish(err error) {
	r.mu.Lock()
	r.done = true
	r.doneErr = err
	subs := r.subs
	r.subs = nil
	r.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

// Done reports whether the runner has finished and any final error.
func (r *Runner) Done() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done, r.doneErr
}

// CommitFunc is the back-end-side implementation of "do the install".
// Wired by main.go because it needs auth.Service, db.Migrate etc.
// The runner calls it synchronously and emits events around each step.
type CommitFunc func(ctx context.Context, in CommitInput, emit func(step, msg, status string)) error

// ApplyRun is a prepared apply that the runner follows.
type ApplyRun func(ctx context.Context, emit func(step, msg, status string)) error

// RestorePrepareFunc keeps the uploaded system backup (and key file) of a
// new install for setup-apply and returns the apply that restores it with
// the server values in v. It runs while the upload is still open.
type RestorePrepareFunc func(v ApplyValues, archive io.Reader, key []byte) (ApplyRun, error)

// Run executes the install in the foreground. Caller typically
// launches this in a goroutine. Closes the runner's subscriber
// channels on return so the SSE handler can detect end-of-stream.
func (r *Runner) Run(ctx context.Context, in CommitInput, fn CommitFunc) {
	emit := func(step, msg, status string) { r.publish(step, msg, status) }
	emit("start", "starting install", "info")

	if err := in.Validate(); err != nil {
		emit("validate", err.Error(), "fail")
		r.finish(err)
		return
	}
	emit("validate", "input validated", "ok")

	if fn == nil {
		err := errors.New("install runner not wired")
		emit("install", err.Error(), "fail")
		r.finish(err)
		return
	}

	if err := fn(ctx, in, emit); err != nil {
		emit("install", "install failed: "+err.Error(), "fail")
		r.finish(err)
		return
	}

	emit("done", "install complete", "done")
	r.finish(nil)
}

// RunApply follows a prepared apply like Run follows an install.
func (r *Runner) RunApply(ctx context.Context, fn ApplyRun) {
	emit := func(step, msg, status string) { r.publish(step, msg, status) }
	emit("start", "starting restore", "info")
	if err := fn(ctx, emit); err != nil {
		emit("install", "restore failed: "+err.Error(), "fail")
		r.finish(err)
		return
	}
	emit("done", "restore complete", "done")
	r.finish(nil)
}

// AddsToDockerGroup reports whether the install adds the service user to
// the docker group. Only a user the wizard creates itself qualifies.
func (in *CommitInput) AddsToDockerGroup() bool {
	return in.ServiceUser.Mode == "create" && in.ServiceUser.AddDocker
}

// CreateSystemUser shells out to `useradd` to create the service user
// when CommitInput.ServiceUser.Mode == "create". Returns silently if
// the user already exists (idempotent — re-running the wizard is
// safe). Skipped on non-linux because useradd is linux-specific.
func CreateSystemUser(username string, addDockerGroup bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("system user creation is only supported on linux")
	}
	// Already exists? no-op.
	out, err := exec.Command("id", "-u", username).Output()
	if err == nil && len(out) > 0 {
		return nil
	}
	cmd := exec.Command("useradd", "--system",
		"--home", "/nonexistent",
		"--shell", "/usr/sbin/nologin",
		username)
	if combined, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("useradd: %s: %w", string(combined), cerr)
	}
	if addDockerGroup {
		cmd := exec.Command("usermod", "-aG", "docker", username)
		if combined, cerr := cmd.CombinedOutput(); cerr != nil {
			return fmt.Errorf("usermod -aG docker: %s: %w", string(combined), cerr)
		}
	}
	return nil
}
