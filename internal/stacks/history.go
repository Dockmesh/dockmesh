package stacks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// DeployHistoryEntry is one row from stack_deploy_history. Returned to
// the UI so users can pick a past deploy to roll back to.
type DeployHistoryEntry struct {
	ID             int64                  `json:"id"`
	// Version is a per-stack counter (1, 2, 3, …) computed at query time
	// via ROW_NUMBER(). The frontend displays this instead of the global
	// auto-increment ID so each stack's history starts at #1, not at
	// whatever the global counter happens to be.
	Version        int                    `json:"version"`
	StackName      string                 `json:"stack_name"`
	HostID         string                 `json:"host_id"`
	ComposeYAML    string                 `json:"compose_yaml,omitempty"` // only populated for the detail GET, not list
	Services       []DeployHistoryService `json:"services,omitempty"`
	Note           string                 `json:"note,omitempty"`
	DeployedBy     string                 `json:"deployed_by,omitempty"`      // users.id (UUID-style)
	DeployedByName string                 `json:"deployed_by_name,omitempty"` // email, populated at query time
	DeployedAt     time.Time              `json:"deployed_at"`
	// P.12.6 extensions (migration 051) — nullable so legacy rows
	// stay readable. Frontend renders an "unknown" badge when Success
	// is nil.
	Success        *bool                  `json:"success,omitempty"`
	DurationMS     *int64                 `json:"duration_ms,omitempty"`
	GitCommitSHA   string                 `json:"git_commit_sha,omitempty"`
	ErrorMessage   string                 `json:"error_message,omitempty"`
}

// DeployHistoryService is the resolved {service → image} pair captured
// at deploy time so operators can see what was actually running.
type DeployHistoryService struct {
	Service string `json:"service"`
	Image   string `json:"image"`
}

// ErrHistoryNotFound is returned when a requested history row doesn't
// exist or belongs to a different stack than the one in the URL.
var ErrHistoryNotFound = errors.New("deploy history entry not found")

// HistoryStore owns the stack_deploy_history table. Kept separate from
// Manager (filesystem) and DeploymentStore (current-host pointer) so
// each piece has a single responsibility. P.12.6.
type HistoryStore struct {
	db *sql.DB
}

func NewHistoryStore(db *sql.DB) *HistoryStore { return &HistoryStore{db: db} }

// Record inserts a new history row. Called from DeployStack after the
// deploy succeeds. Services may be empty if the DeployResult didn't
// enumerate them (remote agent with an old protocol version, say) —
// that's fine, the compose_yaml is the load-bearing field.
//
// Deprecated callers can still use this signature — RecordWithStatus
// is the richer one and back-feeds success/duration/git-sha that
// P.12.6 / migration 051 introduced.
func (s *HistoryStore) Record(ctx context.Context, stackName, hostID, composeYAML, note, userID string, services []DeployHistoryService) (int64, error) {
	return s.RecordWithStatus(ctx, stackName, hostID, composeYAML, note, userID, services, true, 0, "", "")
}

// RecordWithStatus inserts a history row with the success flag,
// duration, optional git commit SHA, and an error message when the
// deploy failed (note + success=false is the typical fail row).
func (s *HistoryStore) RecordWithStatus(ctx context.Context, stackName, hostID, composeYAML, note, userID string, services []DeployHistoryService, success bool, durationMS int64, gitCommitSHA, errorMessage string) (int64, error) {
	var servicesJSON sql.NullString
	if len(services) > 0 {
		b, err := json.Marshal(services)
		if err != nil {
			return 0, err
		}
		servicesJSON = sql.NullString{String: string(b), Valid: true}
	}
	var noteArg sql.NullString
	if note != "" {
		noteArg = sql.NullString{String: note, Valid: true}
	}
	var userArg sql.NullString
	if userID != "" {
		userArg = sql.NullString{String: userID, Valid: true}
	}
	successInt := 0
	if success {
		successInt = 1
	}
	var durArg sql.NullInt64
	if durationMS > 0 {
		durArg = sql.NullInt64{Int64: durationMS, Valid: true}
	}
	var shaArg sql.NullString
	if gitCommitSHA != "" {
		shaArg = sql.NullString{String: gitCommitSHA, Valid: true}
	}
	var errArg sql.NullString
	if errorMessage != "" {
		errArg = sql.NullString{String: errorMessage, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO stack_deploy_history
			(stack_name, host_id, compose_yaml, services_json, note, deployed_by,
			 success, duration_ms, git_commit_sha, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stackName, hostID, composeYAML, servicesJSON, noteArg, userArg,
		successInt, durArg, shaArg, errArg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// List returns the most recent deploys for a stack, newest first.
// Compose YAML is NOT included (list endpoints stay lightweight).
// Limit ≤ 0 means "everything we have, capped at 200 as a safety bound".
//
// The query computes a per-stack version counter via ROW_NUMBER() so
// each stack's deploys are numbered 1, 2, 3, … instead of inheriting
// the global auto-increment ID. The window function partitions on the
// stack and orders by deployed_at ASC so the FIRST deploy gets version 1.
// We sort the outer SELECT DESC so newest comes first in the UI.
func (s *HistoryStore) List(ctx context.Context, stackName string, limit int) ([]DeployHistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id,
		       ROW_NUMBER() OVER (PARTITION BY h.stack_name ORDER BY h.deployed_at ASC, h.id ASC) AS version,
		       h.stack_name, h.host_id, h.services_json, h.note,
		       h.deployed_by, u.email, h.deployed_at,
		       h.success, h.duration_ms, h.git_commit_sha, h.error_message
		FROM stack_deploy_history h
		LEFT JOIN users u ON u.id = h.deployed_by
		WHERE h.stack_name = ?
		ORDER BY h.deployed_at DESC, h.id DESC
		LIMIT ?`, stackName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil slice — callers (and the JSON response) should see `[]`, not
	// `null`, when a stack has no history yet.
	out := make([]DeployHistoryEntry, 0)
	for rows.Next() {
		var e DeployHistoryEntry
		var servicesJSON, note, deployedBy, deployedByEmail, gitSHA, errMsg sql.NullString
		var success sql.NullInt64
		var durationMS sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Version, &e.StackName, &e.HostID, &servicesJSON, &note,
			&deployedBy, &deployedByEmail, &e.DeployedAt,
			&success, &durationMS, &gitSHA, &errMsg); err != nil {
			return nil, err
		}
		if servicesJSON.Valid {
			_ = json.Unmarshal([]byte(servicesJSON.String), &e.Services)
		}
		if note.Valid {
			e.Note = note.String
		}
		if deployedBy.Valid {
			e.DeployedBy = deployedBy.String
		}
		if deployedByEmail.Valid {
			e.DeployedByName = deployedByEmail.String
		}
		if success.Valid {
			ok := success.Int64 == 1
			e.Success = &ok
		}
		if durationMS.Valid {
			d := durationMS.Int64
			e.DurationMS = &d
		}
		if gitSHA.Valid {
			e.GitCommitSHA = gitSHA.String
		}
		if errMsg.Valid {
			e.ErrorMessage = errMsg.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns a single history entry including the full compose_yaml.
// Callers use this to display diffs / confirm before rollback.
// Returns ErrHistoryNotFound when the row doesn't exist or the stack
// in the URL doesn't match the row's stack (defense-in-depth against
// cross-stack ID leakage).
func (s *HistoryStore) Get(ctx context.Context, stackName string, id int64) (*DeployHistoryEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		WITH versioned AS (
			SELECT id,
			       ROW_NUMBER() OVER (PARTITION BY stack_name ORDER BY deployed_at ASC, id ASC) AS version
			FROM stack_deploy_history
			WHERE stack_name = ?
		)
		SELECT h.id, v.version, h.stack_name, h.host_id, h.compose_yaml, h.services_json,
		       h.note, h.deployed_by, u.email, h.deployed_at,
		       h.success, h.duration_ms, h.git_commit_sha, h.error_message
		FROM stack_deploy_history h
		LEFT JOIN users u ON u.id = h.deployed_by
		JOIN versioned v ON v.id = h.id
		WHERE h.id = ? AND h.stack_name = ?`, stackName, id, stackName)
	var e DeployHistoryEntry
	var servicesJSON, note, deployedBy, deployedByEmail, gitSHA, errMsg sql.NullString
	var success sql.NullInt64
	var durationMS sql.NullInt64
	if err := row.Scan(&e.ID, &e.Version, &e.StackName, &e.HostID, &e.ComposeYAML, &servicesJSON,
		&note, &deployedBy, &deployedByEmail, &e.DeployedAt,
		&success, &durationMS, &gitSHA, &errMsg); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrHistoryNotFound
		}
		return nil, err
	}
	if servicesJSON.Valid {
		_ = json.Unmarshal([]byte(servicesJSON.String), &e.Services)
	}
	if note.Valid {
		e.Note = note.String
	}
	if deployedBy.Valid {
		e.DeployedBy = deployedBy.String
	}
	if deployedByEmail.Valid {
		e.DeployedByName = deployedByEmail.String
	}
	if success.Valid {
		ok := success.Int64 == 1
		e.Success = &ok
	}
	if durationMS.Valid {
		d := durationMS.Int64
		e.DurationMS = &d
	}
	if gitSHA.Valid {
		e.GitCommitSHA = gitSHA.String
	}
	if errMsg.Valid {
		e.ErrorMessage = errMsg.String
	}
	return &e, nil
}
