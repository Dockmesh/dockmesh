// Package audit records who did what when. Phase 2 adds a SHA-256 hash
// chain per concept §15.10 so tampering with the DB is detectable — but
// note the doc is explicit: this is tamper-EVIDENT, not tamper-PROOF.
// Real compliance requires exporting the genesis hash to an external
// SIEM/Vault.
package audit

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
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Action constants so callers don't fat-finger strings.
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionRefresh        = "auth.refresh"
	ActionUserCreate     = "user.create"
	ActionUserUpdate     = "user.update"
	ActionUserDelete     = "user.delete"
	ActionUserPassword   = "user.password"
	ActionStackCreate    = "stack.create"
	ActionStackUpdate    = "stack.update"
	ActionStackDelete    = "stack.delete"
	ActionStackDeploy    = "stack.deploy"
	ActionStackStop      = "stack.stop"
	ActionContainerStart    = "container.start"
	ActionContainerStop     = "container.stop"
	ActionContainerKill     = "container.restart"
	ActionContainerRm       = "container.remove"
	ActionContainerUpdate   = "container.update"
	ActionContainerRollback = "container.rollback"
	ActionImagePull      = "image.pull"
	ActionImageRemove    = "image.remove"
	ActionImagePrune     = "image.prune"
	ActionImageScan      = "image.scan"
	ActionNetworkCreate  = "network.create"
	ActionNetworkRemove  = "network.remove"
	ActionVolumeCreate   = "volume.create"
	ActionVolumeRemove   = "volume.remove"
	ActionVolumePrune    = "volume.prune"
	ActionVolumeBrowse   = "volume.browse"
	ActionVolumeReadFile = "volume.read_file"
	ActionGenesis        = "audit.genesis"
)

type Entry struct {
	ID       int64     `json:"id"`
	TS       time.Time `json:"ts"`
	UserID   string    `json:"user_id,omitempty"`
	Username string    `json:"username,omitempty"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Details  string    `json:"details,omitempty"`
	PrevHash string    `json:"prev_hash,omitempty"`
	RowHash  string    `json:"row_hash,omitempty"`
}

// PromRecorder is the audit-side hook into the prometheus collector.
// Interface-typed so the audit package doesn't import internal/metrics
// and create a cycle; main() wires a concrete one at startup.
type PromRecorder interface {
	IncAuditEntry(action string)
}

type Service struct {
	db          *sql.DB
	genesisPath string
	prom        PromRecorder

	mu          sync.Mutex
	lastRowHash string
}

// SetProm attaches a prom recorder after construction. Idempotent;
// nil clears the hook.
func (s *Service) SetProm(p PromRecorder) { s.prom = p }

// NewService wires the DB and loads the genesis hash from disk. Callers
// should subsequently call EnsureGenesis to make sure the chain has a
// starting row.
func NewService(db *sql.DB, genesisPath string) *Service {
	return &Service{db: db, genesisPath: genesisPath}
}

// EnsureGenesis creates the genesis row and seeds lastRowHash. Safe to
// call repeatedly — it's idempotent.
func (s *Service) EnsureGenesis(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Prefer the latest hash that's already in the DB.
	var latest sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT row_hash FROM audit_log WHERE row_hash IS NOT NULL ORDER BY id DESC LIMIT 1`).
		Scan(&latest)
	switch {
	case err == nil && latest.Valid:
		s.lastRowHash = latest.String
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	// No chained row yet — establish genesis.
	genesis, err := s.loadOrCreateGenesisHash()
	if err != nil {
		return err
	}

	// Insert genesis row. prev_hash is the root-of-trust; row_hash of the
	// genesis row equals hash(prev_hash || canonical fields).
	now := time.Now().UTC()
	canon := canonical(genesis, now.Format(time.RFC3339Nano), "", ActionGenesis, "", "")
	rowHash := sha256Hex(canon)

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO audit_log (ts, user_id, action, target, details, prev_hash, row_hash)
		 VALUES (?, NULL, ?, NULL, NULL, ?, ?)`,
		now, ActionGenesis, genesis, rowHash)
	if err != nil {
		return fmt.Errorf("insert genesis: %w", err)
	}
	s.lastRowHash = rowHash
	slog.Warn("audit genesis established — save this hash externally for tamper verification",
		"genesis", genesis, "file", s.genesisPath)
	return nil
}

func (s *Service) loadOrCreateGenesisHash() (string, error) {
	if s.genesisPath == "" {
		return "", errors.New("genesis path not configured")
	}
	if b, err := os.ReadFile(s.genesisPath); err == nil {
		h := string(b)
		if len(h) >= 64 {
			return h[:64], nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Generate a new root-of-trust: hash("dockmesh-genesis-<install-ts>-<nonce>").
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	seed := fmt.Sprintf("dockmesh-genesis-%s-%s",
		time.Now().UTC().Format(time.RFC3339Nano),
		base64.RawStdEncoding.EncodeToString(nonce))
	genesis := sha256Hex(seed)

	if err := os.MkdirAll(filepath.Dir(s.genesisPath), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(s.genesisPath, []byte(genesis+"\n"), 0o400); err != nil {
		return "", err
	}
	return genesis, nil
}

// Write records a chained audit entry. Failures are logged but never
// block the caller.
func (s *Service) Write(ctx context.Context, userID, action, target string, details any) {
	var detailStr string
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detailStr = string(b)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev := s.lastRowHash
	if prev == "" {
		// Should have been seeded by EnsureGenesis; fall back gracefully so
		// we never panic on a broken startup.
		prev = "uninitialized"
	}
	ts := time.Now().UTC()
	canon := canonical(prev, ts.Format(time.RFC3339Nano), userID, action, target, detailStr)
	rowHash := sha256Hex(canon)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (ts, user_id, action, target, details, prev_hash, row_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, nullable(userID), action, nullable(target), nullable(detailStr), prev, rowHash)
	if err != nil {
		slog.Warn("audit write failed", "err", err, "action", action)
		return
	}
	s.lastRowHash = rowHash
	if s.prom != nil {
		s.prom.IncAuditEntry(action)
	}
}

// List returns the most recent entries, newest first.
// ListFilter controls audit log filtering.
type ListFilter struct {
	Limit  int
	Action string // filter by action prefix
	UserID string // filter by user_id
}

func (s *Service) List(ctx context.Context, limit int) ([]Entry, error) {
	return s.ListFiltered(ctx, ListFilter{Limit: limit})
}

func (s *Service) ListFiltered(ctx context.Context, f ListFilter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	query := `SELECT a.id, a.ts, a.user_id, COALESCE(u.username, ''), a.action, a.target, a.details, a.prev_hash, a.row_hash
		FROM audit_log a
		LEFT JOIN users u ON a.user_id = u.id
		WHERE 1=1`
	args := []any{}
	if f.Action != "" {
		query += ` AND a.action LIKE ?`
		args = append(args, f.Action+"%")
	}
	if f.UserID != "" {
		query += ` AND a.user_id = ?`
		args = append(args, f.UserID)
	}
	query += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Entry{}
	for rows.Next() {
		var e Entry
		var userID, username, target, details, prev, row sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &userID, &username, &e.Action, &target, &details, &prev, &row); err != nil {
			return nil, err
		}
		if userID.Valid {
			e.UserID = userID.String
		}
		if username.Valid {
			e.Username = username.String
		}
		if target.Valid {
			e.Target = target.String
		}
		if details.Valid {
			e.Details = details.String
		}
		if prev.Valid {
			e.PrevHash = prev.String
		}
		if row.Valid {
			e.RowHash = row.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyReport summarises a chain verification run.
type VerifyReport struct {
	Verified    int      `json:"verified"`
	Broken      int      `json:"broken"`
	FirstBreak  int64    `json:"first_break,omitempty"`
	BreakReason string   `json:"break_reason,omitempty"`
	Genesis     string   `json:"genesis"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Verify walks the chain from the genesis hash forward, recomputes each
// row_hash and stops at the first mismatch. Legacy rows from before the
// hash-chain migration (prev_hash IS NULL) are counted as warnings but
// not as breaks.
func (s *Service) Verify(ctx context.Context) (*VerifyReport, error) {
	genesis := ""
	if b, err := os.ReadFile(s.genesisPath); err == nil && len(b) >= 64 {
		genesis = string(b[:64])
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, user_id, action, target, details, prev_hash, row_hash
		   FROM audit_log
		  ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	report := &VerifyReport{Genesis: genesis}
	var expectedPrev string
	for rows.Next() {
		var (
			id                                int64
			ts                                time.Time
			userID, target, details           sql.NullString
			action                            string
			prevHash, rowHash                 sql.NullString
		)
		if err := rows.Scan(&id, &ts, &userID, &action, &target, &details, &prevHash, &rowHash); err != nil {
			return nil, err
		}
		if !prevHash.Valid || !rowHash.Valid {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("row %d: legacy entry without chain", id))
			continue
		}
		// On the first chained row, expectedPrev is empty — accept the
		// stored prev_hash as the chain start and remember it as genesis
		// if the file is missing.
		if expectedPrev == "" {
			expectedPrev = prevHash.String
			if genesis == "" {
				report.Genesis = prevHash.String
			}
		}
		if prevHash.String != expectedPrev {
			report.Broken++
			if report.FirstBreak == 0 {
				report.FirstBreak = id
				report.BreakReason = fmt.Sprintf("row %d: prev_hash mismatch (expected %s)", id, expectedPrev[:12])
			}
			return report, nil
		}
		canon := canonical(prevHash.String, ts.UTC().Format(time.RFC3339Nano), nullStr(userID), action, nullStr(target), nullStr(details))
		want := sha256Hex(canon)
		if want != rowHash.String {
			report.Broken++
			if report.FirstBreak == 0 {
				report.FirstBreak = id
				report.BreakReason = fmt.Sprintf("row %d: row_hash mismatch", id)
			}
			return report, nil
		}
		expectedPrev = rowHash.String
		report.Verified++
	}
	return report, rows.Err()
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func canonical(prev, ts, userID, action, target, details string) string {
	return prev + "|" + ts + "|" + userID + "|" + action + "|" + target + "|" + details
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}
