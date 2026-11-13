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
	Status     string    `json:"status"`              // deployed | stopped | migrating | migrated_away | delete_pending
	DeployedAt time.Time `json:"deployed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DeploymentDeletePending marks a stack whose delete waits for its offline
// agent host. The next deploy replaces the status and so cancels the delete.
const DeploymentDeletePending = "delete_pending"

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

// MarkDeletePending sets a stack's status to DeploymentDeletePending if it
// is still deployed on hostID, and reports whether it did. stop records
// whether the delete stops the containers first.
func (s *DeploymentStore) MarkDeletePending(ctx context.Context, stackName, hostID string, stop bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE stack_deployments SET status = ?, delete_stop = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE stack_name = ? AND host_id = ?`, DeploymentDeletePending, stop, stackName, hostID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteStops reports whether the pending delete of a stack stops its
// containers first.
func (s *DeploymentStore) DeleteStops(ctx context.Context, stackName string) (bool, error) {
	var stop bool
	err := s.db.QueryRowContext(ctx, `SELECT delete_stop FROM stack_deployments WHERE stack_name = ?`, stackName).Scan(&stop)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return stop, err
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

// SetTarget records the host a stack was created for. Scope checks use it
// while the stack has no deployment row.
func (s *DeploymentStore) SetTarget(ctx context.Context, stackName, hostID, createdBy string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO stack_targets (stack_name, host_id, created_by) VALUES (?, ?, ?)
		ON CONFLICT(stack_name) DO UPDATE SET host_id = excluded.host_id, created_by = excluded.created_by`,
		stackName, hostID, createdBy)
	return err
}

// Target returns the host a stack was created for, "" when none was given.
func (s *DeploymentStore) Target(ctx context.Context, stackName string) (string, error) {
	var hostID string
	err := s.db.QueryRowContext(ctx, `SELECT host_id FROM stack_targets WHERE stack_name = ?`, stackName).Scan(&hostID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hostID, err
}

// Targets returns every recorded target host keyed by stack name.
func (s *DeploymentStore) Targets(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT stack_name, host_id FROM stack_targets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, hostID string
		if err := rows.Scan(&name, &hostID); err != nil {
			return nil, err
		}
		out[name] = hostID
	}
	return out, rows.Err()
}

// DeleteTarget forgets the target host of a removed stack.
func (s *DeploymentStore) DeleteTarget(ctx context.Context, stackName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stack_targets WHERE stack_name = ?`, stackName)
	return err
}

// HostOf returns the host scope checks use for a stack: its deployment
// host, or the host it was created for while it is not deployed.
func (s *DeploymentStore) HostOf(ctx context.Context, stackName string) (string, error) {
	d, err := s.Get(ctx, stackName)
	if err != nil {
		return "", err
	}
	if d != nil && d.HostID != "" {
		return d.HostID, nil
	}
	return s.Target(ctx, stackName)
}
