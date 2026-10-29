package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// rpcTracer is the tracer every agent-RPC span is attached to. Pulled
// from the global provider so a no-op provider (tracing disabled) is a
// cheap passthrough.
var rpcTracer = otel.Tracer("dockmesh.agent")

// Agent is the public-facing record returned to the UI.
//
// OnlineSince tracks the last status-transition into "online". Reset to
// nil when the agent goes offline/pending/revoked. Lets the UI render a
// real "up Xh" value rather than reusing the misleading enrollment-age
// for it.
type Agent struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Status          string     `json:"status"` // pending | online | offline | revoked
	Version         string     `json:"version,omitempty"`
	OS              string     `json:"os,omitempty"`
	Arch            string     `json:"arch,omitempty"`
	Hostname        string     `json:"hostname,omitempty"`
	DockerVersion   string     `json:"docker_version,omitempty"`
	CertFingerprint string     `json:"cert_fingerprint,omitempty"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	OnlineSince     *time.Time `json:"online_since,omitempty"`
	CertExpiresAt   *time.Time `json:"cert_expires_at,omitempty"`
	// ProtocolVersion and UpdateRecommended describe the live session
	// and are only set while the agent is online.
	ProtocolVersion   int       `json:"protocol_version,omitempty"`
	UpdateRecommended bool      `json:"update_recommended,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CreateResult is what the admin sees right after creating an agent — the
// install command they have to run on the remote host. The plaintext token
// is shown exactly once.
type CreateResult struct {
	Agent          Agent     `json:"agent"`
	Token          string    `json:"token"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	EnrollURL      string    `json:"enroll_url"`
	AgentURL       string    `json:"agent_url"`
	CAFingerprint  string    `json:"ca_fingerprint"`
	InstallHint    string    `json:"install_hint"`
}

var (
	ErrNotFound      = errors.New("agent not found")
	ErrNameTaken     = errors.New("agent name already exists")
	ErrInvalidToken  = errors.New("invalid enrollment token")
	ErrAlreadyOnline = errors.New("agent already connected")
)

// Service owns the agents table + the in-memory map of active connections.
type Service struct {
	db        *sql.DB
	pki       *pki.Manager
	publicURL string // public HTTPS URL of the main dockmesh server
	agentURL  string // wss URL of the mTLS agent listener (e.g. wss://host:8443)
	notifs    *notifications.Service
	enrollTTL time.Duration
	now       func() time.Time

	mu        sync.RWMutex
	connected map[string]*ConnectedAgent // keyed by agent id
}

// SetNotifier wires in the bell-icon notification center so agent
// offline transitions emit an entry. Optional — nil disables.
func (s *Service) SetNotifier(n *notifications.Service) { s.notifs = n }

// ConnectedAgent is held in memory while the agent's WS is open. HTTP
// handlers ask the remote agent to do things via Request() (one-shot
// request/response) or OpenStream() (long-lived multiplexed channel for
// logs / stats / exec).
type ConnectedAgent struct {
	ID       string
	Name     string
	Hello    HelloPayload
	JoinedAt time.Time
	// send is never closed: senders select on done instead, so a frame
	// sent after the session ended fails rather than panics.
	send      chan Frame
	done      chan struct{}
	closeOnce sync.Once
	closeConn func()
	// certFP and certNotAfter describe the client cert this session
	// authenticated with.
	certFP       string
	certNotAfter time.Time
	// renewing allows one renewal in flight, renewed one per session.
	renewing atomic.Bool
	renewed  atomic.Bool

	pendingMu sync.Mutex
	pending   map[string]chan Frame

	streamsMu sync.Mutex
	streams   map[string]*Stream
}

var (
	ErrAgentGone     = errors.New("agent disconnected")
	ErrSendQueueFull = errors.New("agent send queue full")
)

// enqueueTimeout caps how long a sender waits for room in the outbound
// queue of a live but stalled connection.
var enqueueTimeout = 10 * time.Second

func newConnectedAgent(id, name string, closeConn func()) *ConnectedAgent {
	return &ConnectedAgent{
		ID:        id,
		Name:      name,
		JoinedAt:  time.Now(),
		send:      make(chan Frame, 32),
		done:      make(chan struct{}),
		closeConn: closeConn,
	}
}

// Done is closed once the session has ended.
func (c *ConnectedAgent) Done() <-chan struct{} { return c.done }

// shutdown ends the session: senders and waiters unblock and the socket
// closes, which makes the read loop return.
func (c *ConnectedAgent) shutdown() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.closeConn != nil {
			c.closeConn()
		}
	})
}

// Supports reports whether the agent advertised capability name in its
// hello. Agents that predate capability negotiation advertise none.
func (c *ConnectedAgent) Supports(name string) bool {
	for _, have := range c.Hello.Capabilities {
		if have == name {
			return true
		}
	}
	return false
}

// UpdateRecommended is true for agents speaking an older protocol than
// this server; features they lack are skipped or fail with a clear error.
func (c *ConnectedAgent) UpdateRecommended() bool {
	return c.Hello.ProtocolVersion < ProtocolVersion
}

func (c *ConnectedAgent) Send(f Frame) {
	select {
	case <-c.done:
	case c.send <- f:
	default:
		// Drop on full — the agent will time out and reconnect.
	}
}

// enqueue queues f for the outbound pump. It fails once the session has
// ended, the context is done or the queue stayed full for enqueueTimeout.
func (c *ConnectedAgent) enqueue(ctx context.Context, f Frame) error {
	select {
	case <-c.done:
		return ErrAgentGone
	default:
	}
	t := time.NewTimer(enqueueTimeout)
	defer t.Stop()
	select {
	case c.send <- f:
		return nil
	case <-c.done:
		return ErrAgentGone
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return ErrSendQueueFull
	}
}

// requestTimeout bounds the wait for an agent's answer when the caller's
// context has no earlier deadline. Deploys and scale-ups pull images and
// exec_run carries backup pre-hooks such as database dumps, so they get
// far more room than plain reads.
func requestTimeout(frameType string) time.Duration {
	switch frameType {
	case FrameReqContainerExecRun:
		return 30 * time.Minute
	case FrameReqStackDeploy, FrameReqStackScale:
		return 15 * time.Minute
	case FrameReqAgentUpgrade:
		return 11 * time.Minute
	case FrameReqContainerStop, FrameReqContainerRestart, FrameReqContainerRemove,
		FrameReqStackStop, FrameReqImageRemove, FrameReqImagePrune, FrameReqStackCheckScale:
		return 3 * time.Minute
	default:
		return 30 * time.Second
	}
}

// Request sends a request frame and blocks until a response with the
// matching ID arrives, the context is cancelled, the session ends or the
// per-type timeout passes. On cancel or timeout the agent is told to
// abandon the request. The frame's ID is overwritten with a fresh UUID.
func (c *ConnectedAgent) Request(ctx context.Context, f Frame) (*ResponseEnvelope, error) {
	ctx, span := rpcTracer.Start(ctx, "agent.rpc",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("frame.type", f.Type),
			attribute.String("agent.id", c.ID),
			attribute.String("agent.name", c.Name),
		))
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, requestTimeout(f.Type))
	defer cancel()

	id := uuid.NewString()
	f.ID = id

	ch := make(chan Frame, 1)
	c.pendingMu.Lock()
	if c.pending == nil {
		c.pending = make(map[string]chan Frame)
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.enqueue(ctx, f); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("agent request %q: %w", f.Type, err)
	}

	select {
	case resp := <-ch:
		var env ResponseEnvelope
		if err := json.Unmarshal(resp.Payload, &env); err != nil {
			span.SetStatus(codes.Error, "decode response")
			span.RecordError(err)
			return nil, fmt.Errorf("decode response: %w", err)
		}
		if !env.OK {
			span.SetStatus(codes.Error, env.Error)
			span.SetAttributes(attribute.String("agent.error", env.Error))
			return &env, fmt.Errorf("agent: %s", env.Error)
		}
		return &env, nil
	case <-c.done:
		span.SetStatus(codes.Error, ErrAgentGone.Error())
		return nil, fmt.Errorf("agent request %q: %w", f.Type, ErrAgentGone)
	case <-ctx.Done():
		c.cancelRemote(id)
		span.SetStatus(codes.Error, ctx.Err().Error())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("agent request %q timed out: %w", f.Type, ctx.Err())
		}
		return nil, ctx.Err()
	}
}

// cancelRemote asks the agent to abandon request id. Best effort: with a
// full queue or an agent lacking the capability the request runs to the
// end and its late response is discarded.
func (c *ConnectedAgent) cancelRemote(id string) {
	if !c.Supports(CapRequestCancel) {
		return
	}
	select {
	case <-c.done:
	case c.send <- Frame{Type: FrameReqCancel, ID: id}:
	default:
	}
}

// deliverResponse is called by server.go when a res frame arrives on the
// inbound stream. It hands the frame to whichever Request() goroutine is
// waiting for that ID. Frames with no matching pending request are
// dropped silently (e.g. late responses after timeout).
func (c *ConnectedAgent) deliverResponse(f Frame) {
	c.pendingMu.Lock()
	ch, ok := c.pending[f.ID]
	c.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- f:
	default:
	}
}

func NewService(db *sql.DB, p *pki.Manager, publicURL, agentURL string) *Service {
	return &Service{
		db:        db,
		pki:       p,
		publicURL: publicURL,
		agentURL:  agentURL,
		enrollTTL: DefaultEnrollTokenTTL,
		now:       time.Now,
		connected: make(map[string]*ConnectedAgent),
	}
}

// DefaultEnrollTokenTTL is how long an enrollment token stays valid when
// DOCKMESH_AGENT_ENROLL_TOKEN_TTL is not set.
const DefaultEnrollTokenTTL = 24 * time.Hour

// SetEnrollTokenTTL overrides the enrollment-token lifetime. Non-positive
// values keep the default.
func (s *Service) SetEnrollTokenTTL(d time.Duration) {
	if d > 0 {
		s.enrollTTL = d
	}
}

func (s *Service) PublicURL() string { return s.publicURL }
func (s *Service) AgentURL() string  { return s.agentURL }

// CAFingerprint is the SHA-256 of the agents CA the installer pins.
func (s *Service) CAFingerprint() string { return s.pki.CAFingerprint() }

// CACertPEM is the agents CA certificate handed to the installer.
func (s *Service) CACertPEM() []byte { return s.pki.CACertPEM() }

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) Create(ctx context.Context, name string) (*CreateResult, error) {
	if name == "" {
		return nil, errors.New("name required")
	}
	id := uuid.NewString()
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(s.enrollTTL)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agents (id, name, enrollment_token_hash, enrollment_token_expires_at, status)
		VALUES (?, ?, ?, ?, 'pending')`, id, name, hashToken(token), sqliteTime(expires))
	if err != nil {
		return nil, ErrNameTaken
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.createResult(a, token, expires), nil
}

// RotateEnrollToken generates a fresh enrollment token for an existing
// agent, invalidating the previous one. The host keeps its ID, so stacks,
// tags and backup jobs bound to it survive a re-enroll; the old client
// cert stops working once the new enrollment replaces its fingerprint.
func (s *Service) RotateEnrollToken(ctx context.Context, id string) (*CreateResult, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(s.enrollTTL)
	res, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET enrollment_token_hash = ?,
		       enrollment_token_expires_at = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status != 'revoked'`, hashToken(token), sqliteTime(expires), id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.createResult(a, token, expires), nil
}

// ReissueAllEnrollTokens gives every non-revoked agent a fresh
// enrollment token and drops its client cert, in one transaction. Used
// after a CA rotation, when every agent has to enroll again.
func (s *Service) ReissueAllEnrollTokens(ctx context.Context) ([]CreateResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM agents WHERE status != 'revoked' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	expires := s.now().Add(s.enrollTTL)
	tokens := make(map[string]string, len(ids))
	for _, id := range ids {
		token, err := newToken()
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agents
			   SET enrollment_token_hash = ?,
			       enrollment_token_expires_at = ?,
			       cert_fingerprint = NULL,
			       pending_cert_fingerprint = NULL,
			       status = 'pending',
			       updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`, hashToken(token), sqliteTime(expires), id); err != nil {
			return nil, err
		}
		tokens[id] = token
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	out := make([]CreateResult, 0, len(ids))
	for _, id := range ids {
		a, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *s.createResult(a, tokens[id], expires))
	}
	return out, nil
}

func (s *Service) createResult(a *Agent, token string, expires time.Time) *CreateResult {
	fp := s.pki.CAFingerprint()
	// The installer runs as root on the remote host: it creates a
	// dedicated service user, installs Docker if missing and drops a
	// hardened systemd unit. The ca= fingerprint lets the server refuse
	// to render the script once the agents CA has rotated.
	installHint := fmt.Sprintf(
		"curl -fsSL '%s/install/agent.sh?token=%s&ca=%s' | sudo bash",
		s.publicURL, token, fp)
	return &CreateResult{
		Agent:          *a,
		Token:          token,
		TokenExpiresAt: expires.UTC(),
		EnrollURL:      s.EnrollURL(),
		AgentURL:       s.agentURL,
		CAFingerprint:  fp,
		InstallHint:    installHint,
	}
}

func (s *Service) List(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, status, version, os, arch, hostname, docker_version,
		       cert_fingerprint, last_seen_at, online_since, cert_expires_at, created_at, updated_at
		  FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		s.fillStatus(a)
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (*Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, status, version, os, arch, hostname, docker_version,
		       cert_fingerprint, last_seen_at, online_since, cert_expires_at, created_at, updated_at
		  FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.fillStatus(a)
	return a, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.disconnect(id)
	return nil
}

// fillStatus overlays the persisted "status" with the live in-memory state:
// if the agent is currently connected, its status is "online" regardless
// of what the DB says.
func (s *Service) fillStatus(a *Agent) {
	if a.Status == "revoked" {
		return
	}
	s.mu.RLock()
	ag, ok := s.connected[a.ID]
	s.mu.RUnlock()
	if ok {
		a.Status = "online"
		a.ProtocolVersion = ag.Hello.ProtocolVersion
		a.UpdateRecommended = ag.UpdateRecommended()
	} else if a.Status != "pending" {
		a.Status = "offline"
	}
}

// -----------------------------------------------------------------------------
// Enrollment (token → cert exchange)
// -----------------------------------------------------------------------------

func (s *Service) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	if req.Token == "" {
		return nil, ErrInvalidToken
	}
	tokenHash := hashToken(req.Token)
	now := sqliteTime(s.now())

	var id, name string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name FROM agents
		 WHERE enrollment_token_hash = ?
		   AND enrollment_token_expires_at > ?
		   AND status != 'revoked'`,
		tokenHash, now).Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}

	certPEM, keyPEM, fingerprint, err := s.pki.IssueClientCert(id, ClientCertValidity)
	if err != nil {
		return nil, err
	}

	// The token predicate makes the claim atomic: of two concurrent
	// enrollments with the same token only one UPDATE matches a row.
	res, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET enrollment_token_hash = NULL,
		       enrollment_token_expires_at = NULL,
		       cert_fingerprint = ?,
		       pending_cert_fingerprint = NULL,
		       cert_expires_at = ?,
		       status = 'offline',
		       hostname = ?,
		       os = ?,
		       arch = ?,
		       version = ?,
		       docker_version = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?
		   AND enrollment_token_hash = ?
		   AND enrollment_token_expires_at > ?
		   AND status != 'revoked'`,
		fingerprint, sqliteTime(s.now().Add(ClientCertValidity)), req.Hostname, req.OS, req.Arch, req.Version, req.DockerVersion,
		id, tokenHash, now)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrInvalidToken
	}
	// A re-enrolled host replaces its credential; a session still open
	// with the old cert must not outlive it or block the new agent.
	s.disconnect(id)

	return &EnrollResponse{
		AgentID:    id,
		AgentName:  name,
		ClientCert: string(certPEM),
		ClientKey:  string(keyPEM),
		CACert:     string(s.pki.CACertPEM()),
		AgentURL:   s.agentURL,
	}, nil
}

// LookupByFingerprint is what the WS handler uses to resolve "who is this
// connected client" once mTLS has authenticated the cert.
func (s *Service) LookupByFingerprint(ctx context.Context, fingerprint string) (*Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, status, version, os, arch, hostname, docker_version,
		       cert_fingerprint, last_seen_at, online_since, cert_expires_at, created_at, updated_at
		  FROM agents WHERE cert_fingerprint = ? OR pending_cert_fingerprint = ?`, fingerprint, fingerprint)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// -----------------------------------------------------------------------------
// Connection lifecycle (called by server.go)
// -----------------------------------------------------------------------------

func (s *Service) markOnline(ctx context.Context, ag *ConnectedAgent, hello HelloPayload) error {
	s.mu.Lock()
	if _, exists := s.connected[ag.ID]; exists {
		s.mu.Unlock()
		return ErrAlreadyOnline
	}
	s.connected[ag.ID] = ag
	s.mu.Unlock()

	// Set online_since on the transition; preserve a previous value if
	// the agent reconnected within a short window (briefly flapped) by
	// only setting it when currently NULL.
	_, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET status = 'online',
		       hostname = ?,
		       os = ?,
		       arch = ?,
		       version = ?,
		       docker_version = ?,
		       last_seen_at = CURRENT_TIMESTAMP,
		       online_since = COALESCE(online_since, CURRENT_TIMESTAMP),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		hello.Hostname, hello.OS, hello.Arch, hello.Version, hello.DockerVersion, ag.ID)
	if err != nil {
		// The caller doesn't defer markOffline on failure, so the entry
		// would otherwise refuse every later connection as a duplicate.
		s.mu.Lock()
		if s.connected[ag.ID] == ag {
			delete(s.connected, ag.ID)
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Service) markOffline(ag *ConnectedAgent) {
	s.mu.Lock()
	cur, ok := s.connected[ag.ID]
	if ok && cur == ag {
		delete(s.connected, ag.ID)
	}
	s.mu.Unlock()
	if ok && cur != ag {
		// A newer session of the same agent is already online.
		return
	}
	// Clear online_since on offline so the next online transition starts
	// the uptime clock fresh. Compliance + UX both want this behaviour.
	_, _ = s.db.Exec(`UPDATE agents SET status = 'offline', online_since = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, ag.ID)
	if s.notifs != nil {
		_, _ = s.notifs.Emit(context.Background(), notifications.EmitInput{
			Kind:     notifications.KindAgentOffline,
			Severity: notifications.SevWarning,
			Title:    "Agent offline: " + ag.Name,
			Body:     "Heartbeat lost — checking again in a few seconds",
			Link:     "/hosts/" + ag.ID,
		})
	}
}

func (s *Service) touchHeartbeat(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE agents SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

// disconnect ends the live session of agent id, if any. The entry leaves
// the registry right away so a replacement session isn't refused as a
// duplicate while the old read loop winds down.
func (s *Service) disconnect(id string) {
	s.mu.Lock()
	ag := s.connected[id]
	delete(s.connected, id)
	s.mu.Unlock()
	if ag != nil {
		ag.shutdown()
	}
}

// Disconnect forcibly severs the active WebSocket connection (if any)
// for the given agent id. Called by the delete-agent handler so a
// revoked agent can't keep holding an authenticated session. Safe to
// call on ids that aren't currently connected.
func (s *Service) Disconnect(id string) {
	s.disconnect(id)
}

// Connected returns a snapshot of currently-connected agents (for status
// pages / metrics).
func (s *Service) Connected() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.connected))
	for id := range s.connected {
		out = append(out, id)
	}
	return out
}

// GetConnected returns the live ConnectedAgent struct for the given id
// or nil if the agent is not currently connected.
func (s *Service) GetConnected(id string) *ConnectedAgent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected[id]
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgent(r rowScanner) (*Agent, error) {
	var a Agent
	var version, osName, arch, hostname, dockerVersion, fingerprint sql.NullString
	var lastSeen, onlineSince, certExpires sql.NullTime
	if err := r.Scan(&a.ID, &a.Name, &a.Status, &version, &osName, &arch, &hostname,
		&dockerVersion, &fingerprint, &lastSeen, &onlineSince, &certExpires, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if version.Valid {
		a.Version = version.String
	}
	if osName.Valid {
		a.OS = osName.String
	}
	if arch.Valid {
		a.Arch = arch.String
	}
	if hostname.Valid {
		a.Hostname = hostname.String
	}
	if dockerVersion.Valid {
		a.DockerVersion = dockerVersion.String
	}
	if fingerprint.Valid {
		a.CertFingerprint = fingerprint.String
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		a.LastSeenAt = &t
	}
	if onlineSince.Valid {
		t := onlineSince.Time
		a.OnlineSince = &t
	}
	if certExpires.Valid {
		t := certExpires.Time
		a.CertExpiresAt = &t
	}
	return &a, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sqliteTime renders t in the layout SQLite's CURRENT_TIMESTAMP and
// datetime() use, so stored expiry values compare correctly as text.
func sqliteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
