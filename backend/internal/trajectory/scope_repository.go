package trajectory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var errScopeDBUnavailable = errors.New("trajectory scope database unavailable")

// PostgresScopeRepository 基于 trajectory_scopes 表（migrations/900_trajectory_init.sql）。
// 刻意不用 ent，避免与上游的生成代码冲突。
type PostgresScopeRepository struct {
	db *sql.DB
}

func NewPostgresScopeRepository(db *sql.DB) *PostgresScopeRepository {
	return &PostgresScopeRepository{db: db}
}

func (r *PostgresScopeRepository) ListScopes(ctx context.Context) ([]Scope, error) {
	if r == nil || r.db == nil {
		return nil, errScopeDBUnavailable
	}
	rows, err := r.db.QueryContext(ctx, `SELECT scope_type, scope_id, enabled, updated_at FROM trajectory_scopes`)
	if err != nil {
		return nil, fmt.Errorf("list trajectory scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Scope
	for rows.Next() {
		var sc Scope
		var scopeType string
		if err := rows.Scan(&scopeType, &sc.ID, &sc.Enabled, &sc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan trajectory scope: %w", err)
		}
		sc.Type = ScopeType(scopeType)
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (r *PostgresScopeRepository) UpsertScope(ctx context.Context, scope Scope) error {
	if r == nil || r.db == nil {
		return errScopeDBUnavailable
	}
	if err := validateScope(scope.Type, scope.ID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO trajectory_scopes (scope_type, scope_id, enabled, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (scope_type, scope_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = NOW()`,
		string(scope.Type), scope.ID, scope.Enabled)
	if err != nil {
		return fmt.Errorf("upsert trajectory scope: %w", err)
	}
	return nil
}

func (r *PostgresScopeRepository) DeleteScope(ctx context.Context, scopeType ScopeType, id int64) error {
	if r == nil || r.db == nil {
		return errScopeDBUnavailable
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM trajectory_scopes WHERE scope_type = $1 AND scope_id = $2`, string(scopeType), id); err != nil {
		return fmt.Errorf("delete trajectory scope: %w", err)
	}
	return nil
}

func validateScope(scopeType ScopeType, id int64) error {
	switch scopeType {
	case ScopeGlobal:
		if id != 0 {
			return fmt.Errorf("global trajectory scope must use id 0, got %d", id)
		}
	case ScopeGroup, ScopeAPIKey:
		if id <= 0 {
			return fmt.Errorf("%s trajectory scope requires a positive id, got %d", scopeType, id)
		}
	default:
		return fmt.Errorf("unknown trajectory scope type %q", scopeType)
	}
	return nil
}
