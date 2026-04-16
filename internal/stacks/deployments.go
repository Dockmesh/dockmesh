package stacks

import (
	"context"
	"database/sql"
	"time"
)

// Deployment tracks which host a stack is deployed on (P.7). The
// filesystem is still the source of truth for a stack's *content*
// (compose.yaml, .env); this table is purely the runtime-state pointer
// "stack X is deployed on host Y with status Z".
type Deployment struct {
	StackName  string    `json:"stack_name"`
	HostID     string    `json:"host_id"`
	HostName   string    `json:"host_name,omitempty"` // populated at query time, not stored
	Status     string    `json:"status"`              // deployed | stopped | migrating | migrated_away
	DeployedAt time.Time `json:"deployed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DeploymentStore provides CRUD for the stack_deployments table.
// Kept separate from Manager so the filesystem-only Manager doesn't
// grow a database dependency.
type DeploymentStore struct {
	db *sql.DB
}

func NewDeploymentStore(db *sql.DB) *DeploymentStore {
	return &DeploymentStore{db: db}
}

// Set upserts a deployment row. Used by DeployStack (status=deployed)
// and StopStack (status=stopped).
func (s *DeploymentStore) Set(ctx context.Context, stackName, hostID, status string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO stack_deployments (stack_name, host_id, status, deployed_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(stack_name) DO UPDATE SET
			host_id     = excluded.host_id,
			status      = excluded.status,
			deployed_at = CASE WHEN excluded.status = 'deployed' THEN CURRENT_TIMESTAMP ELSE stack_deployments.deployed_at END,
			updated_at  = CURRENT_TIMESTAMP`,
		stackName, hostID, status)
	return err
}

// Delete removes a deployment row. Called by DeleteStack.
func (s *DeploymentStore) Delete(ctx context.Context, stackName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stack_deployments WHERE stack_name = ?`, stackName)
	return err
}

// Get returns the deployment for a single stack, or nil if none.
func (s *DeploymentStore) Get(ctx context.Context, stackName string) (*Deployment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT stack_name, host_id, status, deployed_at, updated_at
		FROM stack_deployments WHERE stack_name = ?`, stackName)
	d, err := scanDeployment(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// All returns every deployment row keyed by stack name.
func (s *DeploymentStore) All(ctx context.Context) (map[string]*Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, host_id, status, deployed_at, updated_at
		FROM stack_deployments ORDER BY stack_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*Deployment)
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out[d.StackName] = d
	}
	return out, rows.Err()
}

// ByHost returns all deployments for a given host.
func (s *DeploymentStore) ByHost(ctx context.Context, hostID string) ([]*Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, host_id, status, deployed_at, updated_at
		FROM stack_deployments WHERE host_id = ? ORDER BY stack_name`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type deploymentScanner interface {
	Scan(dest ...any) error
}

func scanDeployment(r deploymentScanner) (*Deployment, error) {
	var d Deployment
	if err := r.Scan(&d.StackName, &d.HostID, &d.Status, &d.DeployedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	return &d, nil
}
