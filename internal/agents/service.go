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
	"time"

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
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// CreateResult is what the admin sees right after creating an agent — the
// install command they have to run on the remote host. The plaintext token
// is shown exactly once.
type CreateResult struct {
	Agent       Agent  `json:"agent"`
	Token       string `json:"token"`
	EnrollURL   string `json:"enroll_url"`
	AgentURL    string `json:"agent_url"`
	InstallHint string `json:"install_hint"`
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

	mu        sync.RWMutex
	connected map[string]*ConnectedAgent // keyed by agent id
}

// ConnectedAgent is held in memory while the agent's WS is open. HTTP
// handlers ask the remote agent to do things via Request() (one-shot
// request/response) or OpenStream() (long-lived multiplexed channel for
// logs / stats / exec).
type ConnectedAgent struct {
	ID       string
	Name     string
	Hello    HelloPayload
	JoinedAt time.Time
	send     chan Frame

	pendingMu sync.Mutex
	pending   map[string]chan Frame

	streamsMu sync.Mutex
	streams   map[string]*Stream
}

func (c *ConnectedAgent) Send(f Frame) {
	select {
	case c.send <- f:
	default:
		// Drop on full — the agent will time out and reconnect.
	}
}

// Request sends a request frame and blocks until a response with the
// matching ID arrives, the context is cancelled, or the request times
// out (30s). The frame's ID is overwritten with a fresh UUID.
func (c *ConnectedAgent) Request(ctx context.Context, f Frame) (*ResponseEnvelope, error) {
	ctx, span := rpcTracer.Start(ctx, "agent.rpc",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("frame.type", f.Type),
			attribute.String("agent.id", c.ID),
			attribute.String("agent.name", c.Name),
		))
	defer span.End()

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

	select {
	case c.send <- f:
	case <-ctx.Done():
		span.SetStatus(codes.Error, ctx.Err().Error())
		return nil, ctx.Err()
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
	case <-ctx.Done():
		span.SetStatus(codes.Error, ctx.Err().Error())
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		span.SetStatus(codes.Error, "timeout")
		return nil, fmt.Errorf("agent request %q timed out", f.Type)
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
		connected: make(map[string]*ConnectedAgent),
	}
}

func (s *Service) PublicURL() string { return s.publicURL }
func (s *Service) AgentURL() string  { return s.agentURL }

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
	tokenHash := hashToken(token)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agents (id, name, enrollment_token_hash, status)
		VALUES (?, ?, ?, 'pending')`, id, name, tokenHash)
	if err != nil {
		return nil, ErrNameTaken
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// One-line installer command — runs on the remote host as root,
	// creates a dedicated dockmesh-agent service user, installs Docker
	// if missing, drops a hardened systemd unit and starts the agent.
	installHint := fmt.Sprintf(
		"curl -fsSL %s/install/agent.sh?token=%s | sudo bash",
		s.publicURL, token)

	return &CreateResult{
		Agent:       *a,
		Token:       token,
		EnrollURL:   s.publicURL + "/api/v1/agents/enroll",
		AgentURL:    s.agentURL,
		InstallHint: installHint,
	}, nil
}

func (s *Service) List(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, status, version, os, arch, hostname, docker_version,
		       cert_fingerprint, last_seen_at, created_at, updated_at
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
		       cert_fingerprint, last_seen_at, created_at, updated_at
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
	_, ok := s.connected[a.ID]
	s.mu.RUnlock()
	if ok {
		a.Status = "online"
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

	var id, name string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name FROM agents WHERE enrollment_token_hash = ? AND status = 'pending'`,
		tokenHash).Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}

	certPEM, keyPEM, fingerprint, err := s.pki.IssueClientCert(id, 365*24*time.Hour)
	if err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE agents
		   SET enrollment_token_hash = NULL,
		       cert_fingerprint = ?,
		       status = 'offline',
		       hostname = ?,
		       os = ?,
		       arch = ?,
		       version = ?,
		       docker_version = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		fingerprint, req.Hostname, req.OS, req.Arch, req.Version, req.DockerVersion, id)
	if err != nil {
		return nil, err
	}

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
		       cert_fingerprint, last_seen_at, created_at, updated_at
		  FROM agents WHERE cert_fingerprint = ?`, fingerprint)
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

	_, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET status = 'online',
		       hostname = ?,
		       os = ?,
		       arch = ?,
		       version = ?,
		       docker_version = ?,
		       last_seen_at = CURRENT_TIMESTAMP,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		hello.Hostname, hello.OS, hello.Arch, hello.Version, hello.DockerVersion, ag.ID)
	return err
}

func (s *Service) markOffline(ag *ConnectedAgent) {
	s.mu.Lock()
	if cur, ok := s.connected[ag.ID]; ok && cur == ag {
		delete(s.connected, ag.ID)
	}
	s.mu.Unlock()
	_, _ = s.db.Exec(`UPDATE agents SET status = 'offline', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, ag.ID)
}

func (s *Service) touchHeartbeat(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE agents SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

func (s *Service) disconnect(id string) {
	s.mu.Lock()
	ag, ok := s.connected[id]
	if ok {
		delete(s.connected, id)
	}
	s.mu.Unlock()
	if ag != nil {
		close(ag.send)
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
	var lastSeen sql.NullTime
	if err := r.Scan(&a.ID, &a.Name, &a.Status, &version, &osName, &arch, &hostname,
		&dockerVersion, &fingerprint, &lastSeen, &a.CreatedAt, &a.UpdatedAt); err != nil {
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
	return &a, nil
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
