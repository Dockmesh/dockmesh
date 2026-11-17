package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Stacks directory request states of a host, as GET /agents shows them.
const (
	StacksDirStatePending = "pending"
	StacksDirStateApplied = "applied"
	StacksDirStateRefused = "refused"
)

// StacksDirState is the stacks directory last asked for and how the host
// answered.
type StacksDirState struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// CreateOptions are the optional fields of a new host. An empty Name
// takes the hostname the agent reports at enrollment; StacksDir is
// applied when the agent first connects.
type CreateOptions struct {
	Name      string
	StacksDir string
}

// ErrInvalidStacksDir is a stacks directory the server refuses before
// asking the agent; the agent checks everything that needs the host.
var ErrInvalidStacksDir = errors.New("stacks directory must be an absolute, clean path of letters, digits and ._-/")

// maxStacksDirLen matches what the installer and the agent accept.
const maxStacksDirLen = 4096

// stacksDirPattern is the character set install-agent.sh accepts for
// --stacks-dir, so a path set here also works as an installer flag.
var stacksDirPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// ValidateStacksDir checks what the server can check without the host.
func ValidateStacksDir(p string) error {
	if len(p) > maxStacksDirLen || !stacksDirPattern.MatchString(p) || path.Clean(p) != p || p == "/" {
		return ErrInvalidStacksDir
	}
	return nil
}

// WaitingForSetupError refuses work on a host whose stacks directory was
// never confirmed.
type WaitingForSetupError struct{ Name string }

func (e *WaitingForSetupError) Error() string {
	return fmt.Sprintf("Host %s is waiting for setup. Choose its stacks directory first: Hosts › %s › Setup.", e.Name, e.Name)
}

// ErrWaitingForSetup matches every WaitingForSetupError.
var ErrWaitingForSetup = errors.New("host is waiting for setup")

func (e *WaitingForSetupError) Is(target error) bool { return target == ErrWaitingForSetup }

// CreateWith creates a host that waits for setup until its stacks
// directory is confirmed.
func (s *Service) CreateWith(ctx context.Context, o CreateOptions) (*CreateResult, error) {
	o.Name = strings.TrimSpace(o.Name)
	if o.StacksDir != "" {
		if err := ValidateStacksDir(o.StacksDir); err != nil {
			return nil, err
		}
	}
	id := uuid.NewString()
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(s.enrollTTL)
	name, fromHost := o.Name, false
	if name == "" {
		name, fromHost = "new-host-"+id[:8], true
	}
	var sdPath, sdStatus sql.NullString
	if o.StacksDir != "" {
		sdPath = sql.NullString{String: o.StacksDir, Valid: true}
		sdStatus = sql.NullString{String: StacksDirStatePending, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agents (id, name, enrollment_token_hash, enrollment_token_expires_at, status,
		                    needs_setup, name_from_host, stacks_dir_path, stacks_dir_status)
		VALUES (?, ?, ?, ?, 'pending', 1, ?, ?, ?)`,
		id, name, hashToken(token), sqliteTime(expires), fromHost, sdPath, sdStatus)
	if err != nil {
		return nil, ErrNameTaken
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.createResult(a, token, expires), nil
}

// hostnamePattern is what a reported hostname must look like to become a
// host's name; anything else keeps the generated name.
var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// nameFromHostname gives a host created without a name the hostname its
// agent reported, or hostname-2, -3 … when that name is taken, and
// returns the name the host has afterwards. "local" is the central host's
// name in the host switcher and is never taken from an agent.
func (s *Service) nameFromHostname(ctx context.Context, id, current, hostname string) string {
	if hostnamePattern.MatchString(hostname) && !strings.EqualFold(hostname, "local") {
		for i := 1; i <= 20; i++ {
			name := hostname
			if i > 1 {
				name = hostname + "-" + strconv.Itoa(i)
			}
			_, err := s.db.ExecContext(ctx, `
				UPDATE agents SET name = ?, name_from_host = 0, updated_at = CURRENT_TIMESTAMP
				 WHERE id = ? AND name_from_host = 1`, name, id)
			if err == nil {
				slog.Info("host named after its hostname", "id", id, "from", current, "name", name)
				return name
			}
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				slog.Warn("name host after its hostname", "id", id, "err", err)
				return current
			}
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET name_from_host = 0 WHERE id = ?`, id); err != nil {
		slog.Warn("keep generated host name", "id", id, "err", err)
	}
	return current
}

// SetupError is a WaitingForSetupError when host id waits for setup.
func (s *Service) SetupError(ctx context.Context, id string) error {
	var name string
	var needs bool
	err := s.db.QueryRowContext(ctx, `SELECT name, needs_setup FROM agents WHERE id = ?`, id).Scan(&name, &needs)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if needs {
		return &WaitingForSetupError{Name: name}
	}
	return nil
}

// ConfirmSetup ends "Waiting for setup" of host id.
func (s *Service) ConfirmSetup(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agents SET needs_setup = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND needs_setup = 1`, id)
	return err
}

// setupFromHello settles the setup of a host that waits for it from what
// its agent reports: a stacks directory set at install, or the one asked
// for that the agent uses now. It returns the path still to apply.
func (s *Service) setupFromHello(ctx context.Context, id string, hello HelloPayload) (string, error) {
	var needs bool
	var sdPath, sdStatus sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT needs_setup, stacks_dir_path, stacks_dir_status FROM agents WHERE id = ?`, id).
		Scan(&needs, &sdPath, &sdStatus)
	if err != nil {
		return "", err
	}
	pending := sdStatus.String == StacksDirStatePending
	if pending && currentStacksDir(hello) == sdPath.String {
		return "", s.recordStacksDir(ctx, id, sdPath.String, StacksDirStateApplied, "")
	}
	// A pending request for another directory decides the setup, so no
	// deploy lands in the install's directory just before the switch.
	if needs && hello.StacksDirSet && !pending {
		if err := s.ConfirmSetup(ctx, id); err != nil {
			return "", err
		}
	}
	if pending {
		return sdPath.String, nil
	}
	return "", nil
}

func currentStacksDir(hello HelloPayload) string {
	for _, d := range hello.ReportedStacksDirs() {
		if d.Current {
			return d.Path
		}
	}
	return ""
}

// recordStacksDir stores the outcome of a stacks directory request; an
// applied one confirms the setup. A newer request for another path is
// left alone.
func (s *Service) recordStacksDir(ctx context.Context, id, p, status, msg string) error {
	var errCol sql.NullString
	if len(msg) > maxStacksDirError {
		msg = strings.ToValidUTF8(msg[:maxStacksDirError], "")
	}
	if msg != "" {
		errCol = sql.NullString{String: msg, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET stacks_dir_status = ?,
		       stacks_dir_error = ?,
		       needs_setup = CASE WHEN ? = 'applied' THEN 0 ELSE needs_setup END,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND stacks_dir_path = ?`, status, errCol, status, id, p)
	return err
}

// maxStacksDirError cuts the reason an agent gives for a refusal.
const maxStacksDirError = 512

// stacksDirApplyWait covers the agent's own minute of waiting for root.
const stacksDirApplyWait = 3 * time.Minute

// applyStacksDir asks the connected agent ag to switch to p and records
// its answer. Root restarts a managed agent once it switched, so the
// answer can be lost; the next hello then shows the new directory.
func (s *Service) applyStacksDir(ctx context.Context, ag *ConnectedAgent, p string) {
	if !ag.applying.CompareAndSwap(false, true) {
		return
	}
	s.applyClaimed(ctx, ag, p)
}

// applyClaimed is applyStacksDir for a caller that holds ag.applying; it
// releases it.
func (s *Service) applyClaimed(ctx context.Context, ag *ConnectedAgent, p string) {
	defer ag.applying.Store(false)
	payload, _ := json.Marshal(StacksDirApplyReq{Path: p})
	ctx, cancel := context.WithTimeout(ctx, stacksDirApplyWait)
	defer cancel()
	env, err := ag.Request(ctx, Frame{Type: FrameReqStacksDirApply, Payload: payload})
	if env != nil && !env.OK {
		if err := s.recordStacksDir(context.WithoutCancel(ctx), ag.ID, p, StacksDirStateRefused, env.Error); err != nil {
			slog.Warn("stacks dir apply: record", "agent", ag.Name, "err", err)
		}
		return
	}
	if err != nil {
		slog.Info("stacks dir apply: no answer yet", "agent", ag.Name, "path", p, "err", err)
		return
	}
	var res StacksDirApplyRes
	if err := json.Unmarshal(env.Data, &res); err != nil {
		slog.Warn("stacks dir apply: bad answer", "agent", ag.Name, "err", err)
		return
	}
	var rerr error
	switch res.Status {
	case StacksDirDone:
		rerr = s.recordStacksDir(context.WithoutCancel(ctx), ag.ID, p, StacksDirStateApplied, "")
	case StacksDirRefused, StacksDirFailed:
		reason := res.Reason
		if reason == "" {
			reason = res.Check.Message
		}
		rerr = s.recordStacksDir(context.WithoutCancel(ctx), ag.ID, p, StacksDirStateRefused, reason)
	}
	if rerr != nil {
		slog.Warn("stacks dir apply: record", "agent", ag.Name, "err", rerr)
	}
}

var (
	// ErrLegacyLayout: only the root updater of a managed agent can switch
	// the stacks directory.
	ErrLegacyLayout = errors.New("changing the stacks directory needs the managed layout; run the install command on the host again")
	// ErrApplyRunning: the host is still answering an earlier request.
	ErrApplyRunning = errors.New("the host is still applying a stacks directory")
)

// SetStacksDir makes p the stacks directory of the connected host id.
// The directory the agent uses now is confirmed at once (applied true),
// which also ends "Waiting for setup"; any other path is sent to the
// agent and its answer recorded in the background.
func (s *Service) SetStacksDir(ctx context.Context, id, p string) (applied bool, err error) {
	if err := ValidateStacksDir(p); err != nil {
		return false, err
	}
	ag := s.GetConnected(id)
	if ag == nil {
		return false, ErrAgentNotConnected
	}
	current := currentStacksDir(ag.Hello) == p
	if !current && ag.Hello.HelloLayout() != LayoutManaged {
		return false, ErrLegacyLayout
	}
	// Claimed before any write, so the recorded request is always the one
	// the agent works on.
	if !ag.applying.CompareAndSwap(false, true) {
		return false, ErrApplyRunning
	}
	if current {
		defer ag.applying.Store(false)
		_, err := s.db.ExecContext(ctx, `
			UPDATE agents
			   SET stacks_dir_path = ?, stacks_dir_status = 'applied', stacks_dir_error = NULL,
			       needs_setup = 0, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`, p, id)
		return err == nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET stacks_dir_path = ?, stacks_dir_status = 'pending', stacks_dir_error = NULL,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, p, id); err != nil {
		ag.applying.Store(false)
		return false, err
	}
	go s.applyClaimed(context.WithoutCancel(ctx), ag, p)
	return false, nil
}

// CheckStacksDir asks the connected host id to check p with the rules its
// root updater applies, without changing anything.
func (s *Service) CheckStacksDir(ctx context.Context, id, p string) (StacksDirCheck, error) {
	if err := ValidateStacksDir(p); err != nil {
		return StacksDirCheck{}, err
	}
	ag := s.GetConnected(id)
	if ag == nil {
		return StacksDirCheck{}, ErrAgentNotConnected
	}
	payload, _ := json.Marshal(StacksDirApplyReq{Path: p, CheckOnly: true})
	env, err := ag.Request(ctx, Frame{Type: FrameReqStacksDirApply, Payload: payload})
	if err != nil {
		return StacksDirCheck{}, err
	}
	var res StacksDirApplyRes
	if err := json.Unmarshal(env.Data, &res); err != nil {
		return StacksDirCheck{}, fmt.Errorf("decode stacks dir check: %w", err)
	}
	if len(res.Check.Message) > maxStacksDirError {
		res.Check.Message = strings.ToValidUTF8(res.Check.Message[:maxStacksDirError], "")
	}
	return res.Check, nil
}
