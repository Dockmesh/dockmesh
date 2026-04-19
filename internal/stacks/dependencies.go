package stacks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrDependencyCycle is returned by DependencyStore.Set when the
// requested edge would close a cycle in the dependency graph.
var ErrDependencyCycle = errors.New("dependency cycle")

// DependencyStore owns the stack_dependencies table. P.12.7.
type DependencyStore struct {
	db *sql.DB
}

func NewDependencyStore(db *sql.DB) *DependencyStore { return &DependencyStore{db: db} }

// Get returns the direct dependencies of a stack (stacks that must be
// running before this one can deploy). Not recursive — callers doing
// order-resolution should walk the graph themselves via TopoOrder.
func (s *DependencyStore) Get(ctx context.Context, stackName string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT depends_on FROM stack_dependencies
		WHERE stack_name = ?
		ORDER BY depends_on`, stackName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Always return a non-nil slice so the JSON response is [], not null.
	out := make([]string, 0)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Dependents returns the stacks that depend on this one — the other
// direction of the edge. Used on DeleteStack to warn operators that
// removing this stack will break others.
func (s *DependencyStore) Dependents(ctx context.Context, stackName string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name FROM stack_dependencies
		WHERE depends_on = ?
		ORDER BY stack_name`, stackName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Set replaces the complete set of direct dependencies for a stack.
// Runs cycle detection against the *post-write* graph: the check
// passes only if no dep transitively depends on stackName. Idempotent
// and transactional — either the whole new set lands or nothing does.
func (s *DependencyStore) Set(ctx context.Context, stackName string, deps []string) error {
	// De-dup + self-reference guard.
	seen := make(map[string]struct{}, len(deps))
	clean := make([]string, 0, len(deps))
	for _, d := range deps {
		if d == "" || d == stackName {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		clean = append(clean, d)
	}

	// Cycle detection: simulate the new edges and walk from each dep.
	// If any dep transitively reaches stackName, reject.
	existing, err := s.allEdges(ctx)
	if err != nil {
		return err
	}
	// Strip the current stack's outgoing edges from the baseline, then
	// overlay the new ones.
	graph := make(map[string][]string, len(existing))
	for from, tos := range existing {
		if from == stackName {
			continue
		}
		graph[from] = append([]string(nil), tos...)
	}
	graph[stackName] = clean
	for _, d := range clean {
		if reaches(graph, d, stackName) {
			return fmt.Errorf("%w: %s -> %s would close a loop", ErrDependencyCycle, stackName, d)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM stack_dependencies WHERE stack_name = ?`, stackName); err != nil {
		return err
	}
	for _, d := range clean {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO stack_dependencies (stack_name, depends_on)
			VALUES (?, ?)`, stackName, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAll removes every edge involving this stack — both outbound
// (this stack depending on others) and inbound (others depending on
// this one). Called from DeleteStack so orphan edges don't linger.
func (s *DependencyStore) DeleteAll(ctx context.Context, stackName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM stack_dependencies
		WHERE stack_name = ? OR depends_on = ?`, stackName, stackName)
	return err
}

// TopoOrder returns the stacks that must be deployed, in deploy order,
// for `stackName` to be satisfiable. The result includes stackName
// itself as the last element. Cycles return ErrDependencyCycle.
//
// Output is deterministic: alphabetical within a dependency level, so
// the same graph always produces the same order and audit logs stay
// readable across deploys.
func (s *DependencyStore) TopoOrder(ctx context.Context, stackName string) ([]string, error) {
	all, err := s.allEdges(ctx)
	if err != nil {
		return nil, err
	}
	visited := make(map[string]bool)
	onStack := make(map[string]bool)
	out := make([]string, 0)
	var visit func(n string) error
	visit = func(n string) error {
		if onStack[n] {
			return fmt.Errorf("%w at %s", ErrDependencyCycle, n)
		}
		if visited[n] {
			return nil
		}
		onStack[n] = true
		deps := append([]string(nil), all[n]...)
		// Alphabetical for determinism.
		sortStrings(deps)
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		onStack[n] = false
		visited[n] = true
		out = append(out, n)
		return nil
	}
	if err := visit(stackName); err != nil {
		return nil, err
	}
	return out, nil
}

// allEdges loads the full edge set once so repeated reachability /
// topo walks don't hit the DB for every node.
func (s *DependencyStore) allEdges(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stack_name, depends_on FROM stack_dependencies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return nil, err
		}
		out[from] = append(out[from], to)
	}
	return out, rows.Err()
}

// reaches reports whether target is reachable from start in the given
// adjacency-list graph (DFS). Used by Set for cycle detection.
func reaches(graph map[string][]string, start, target string) bool {
	if start == target {
		return true
	}
	stack := []string{start}
	seen := map[string]bool{start: true}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, next := range graph[n] {
			if next == target {
				return true
			}
			if !seen[next] {
				seen[next] = true
				stack = append(stack, next)
			}
		}
	}
	return false
}

// sortStrings is a tiny helper so we don't pull in sort for the single
// call site. In-place insertion sort; the slices are always tiny
// (direct deps of a single stack), so algorithm doesn't matter.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
