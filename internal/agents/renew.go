package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
)

const (
	// ClientCertValidity is the lifetime of enrolled and renewed agent
	// client certs.
	ClientCertValidity = 365 * 24 * time.Hour
	// RenewBefore is how close to expiry an agent asks for a new cert.
	// The server refuses renewals for certs further from expiry.
	RenewBefore = 30 * 24 * time.Hour
	// certNoticeBefore is when admins are told about a cert that hasn't
	// renewed itself.
	certNoticeBefore = 14 * 24 * time.Hour

	certNoticePrefix = "agents.cert-expiry."
)

var errRenewNotDue = errors.New("certificate is not due for renewal")

// recordCert stores the expiry of the cert an agent connected with. A
// connection with the renewed cert makes it the current one, which
// retires the old cert.
func (s *Service) recordCert(ctx context.Context, id, fingerprint string, notAfter time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET cert_fingerprint = CASE WHEN pending_cert_fingerprint = ? THEN ? ELSE cert_fingerprint END,
		       pending_cert_fingerprint = CASE WHEN pending_cert_fingerprint = ? THEN NULL ELSE pending_cert_fingerprint END,
		       cert_expires_at = ?
		 WHERE id = ?`,
		fingerprint, fingerprint, fingerprint, sqliteTime(notAfter), id)
	return err
}

// handleCertRenew answers an agent.cert_renew frame on the agent's own
// session, so the agent is already authenticated by its current cert.
func (s *Service) handleCertRenew(ctx context.Context, ag *ConnectedAgent, f Frame) {
	env := ResponseEnvelope{OK: true}
	res, err := s.renewCert(ctx, ag, f.Payload)
	if err != nil {
		slog.Warn("agent cert renewal refused", "agent", ag.Name, "err", err)
		env = ResponseEnvelope{OK: false, Error: err.Error()}
	} else {
		env.Data, _ = json.Marshal(res)
		slog.Info("agent cert renewed", "agent", ag.Name)
	}
	payload, _ := json.Marshal(env)
	_ = ag.enqueue(ctx, Frame{Type: FrameRes, ID: f.ID, Payload: payload})
}

func (s *Service) renewCert(ctx context.Context, ag *ConnectedAgent, raw json.RawMessage) (*CertRenewRes, error) {
	if ag.certNotAfter.Sub(s.now()) > RenewBefore {
		return nil, errRenewNotDue
	}
	var req CertRenewReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode renew request: %w", err)
	}
	certPEM, fingerprint, _, err := s.pki.SignClientCSR([]byte(req.CSR), ag.ID, ClientCertValidity)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE agents
		   SET pending_cert_fingerprint = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status != 'revoked'`, fingerprint, ag.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	return &CertRenewRes{ClientCert: string(certPEM)}, nil
}

// RunCertNotices keeps the cert-expiry notices current until ctx ends.
func (s *Service) RunCertNotices(ctx context.Context, n *notices.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.SyncCertNotices(ctx, n); err != nil && ctx.Err() == nil {
			slog.Warn("agent cert notices", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncCertNotices raises a notice for every agent whose cert expires
// within certNoticeBefore, critical once an offline agent's cert has
// expired, and resolves notices whose condition is gone.
func (s *Service) SyncCertNotices(ctx context.Context, n *notices.Service) error {
	list, err := s.List(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	want := map[string]notices.RaiseInput{}
	for _, a := range list {
		if a.CertExpiresAt == nil || a.Status == "revoked" || a.Status == "pending" {
			continue
		}
		left := a.CertExpiresAt.Sub(now)
		if left > certNoticeBefore {
			continue
		}
		key := certNoticePrefix + a.ID
		date := a.CertExpiresAt.UTC().Format("2006-01-02")
		in := notices.RaiseInput{
			Key:      key,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    "Agent certificate expires soon: " + a.Name,
			Body: "The client certificate of " + a.Name + " expires on " + date + ". Current agents renew it " +
				"automatically while online; update this agent, or re-issue its enrollment token on the host page " +
				"and re-run the installer on that host. The host keeps its ID, stacks and backups.",
			Link: "/hosts/" + a.ID,
		}
		if left <= 0 {
			if a.Status == "online" {
				continue
			}
			in.Severity = notices.SevCritical
			in.Title = "Agent certificate expired: " + a.Name
			in.Body = a.Name + " can no longer connect because its client certificate expired on " + date +
				". Re-issue the enrollment token on the host page and re-run the installer on that host. " +
				"The host keeps its ID, stacks and backups."
		}
		want[key] = in
	}

	open, err := n.Open(ctx)
	if err != nil {
		return err
	}
	for _, o := range open {
		if !strings.HasPrefix(o.Key, certNoticePrefix) {
			continue
		}
		if in, ok := want[o.Key]; ok && in.Severity == o.Severity {
			delete(want, o.Key)
			continue
		}
		// Severity changed (expired meanwhile) or the condition is gone.
		if err := n.Resolve(ctx, o.Key); err != nil {
			return err
		}
	}
	for _, in := range want {
		if err := n.Raise(ctx, in); err != nil {
			return err
		}
	}
	return nil
}
