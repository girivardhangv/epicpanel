package websites

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Dynamic-state constants (websites.dynamic_state).
const (
	DynStateActive  = "active"
	DynStateBusy    = "busy"
	DynStateAttacked = "suspended_attack"
)

// ============================================================================
// Dynamic resources (traffic-adaptive allocation + bot defense) and the
// Free Perk overlay. The allocator (internal/api/dynamic.go) is the only
// writer of tier/state besides these API-level toggles; every automated
// transition also lands a row in dynamic_resource_events (written by the
// caller so store stays free of policy).
// ============================================================================

// ListDynamicReady returns ready websites opted into dynamic allocation
// (the allocator's scan set).
func (s *Store) ListDynamicReady(ctx context.Context, limit int) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM websites
		WHERE dynamic_enabled AND status = 'ready'
		ORDER BY created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanList(rows)
}

// ListDynamicEnabled returns ALL non-deleted websites opted in regardless of
// status (used by the global-off sweep and restore paths).
func (s *Store) ListDynamicEnabled(ctx context.Context, limit int) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM websites
		WHERE dynamic_enabled AND status NOT IN ('deleted', 'deleting')
		ORDER BY created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanList(rows)
}

// SetDynamicState persists tier + state in one guarded update (the pair is
// meaningful only together — a tier without its state would confuse the
// payload builder).
func (s *Store) SetDynamicState(ctx context.Context, id uuid.UUID, tier int, state string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET dynamic_tier = $2, dynamic_state = $3, updated_at = now()
		WHERE id = $1 AND status NOT IN ('deleted', 'deleting')`, id, tier, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDynamicEnabled flips the per-site feature toggle. Disabling also clears
// tier/state so a later re-enable starts from the package base.
func (s *Store) SetDynamicEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites
		SET dynamic_enabled = $2,
		    dynamic_tier = CASE WHEN $2 THEN dynamic_tier ELSE 0 END,
		    dynamic_state = CASE WHEN $2 THEN dynamic_state ELSE 'active' END,
		    updated_at = now()
		WHERE id = $1 AND status NOT IN ('deleted', 'deleting')`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetFreePerk assigns/removes the Free Perk overlay on one website.
func (s *Store) SetFreePerk(ctx context.Context, id uuid.UUID, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET free_perk = $2, updated_at = now()
		WHERE id = $1 AND status NOT IN ('deleted', 'deleting')`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountFreePerk counts active perk sites in an org (cap enforcement).
func (s *Store) CountFreePerk(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM websites
		WHERE organization_id = $1 AND free_perk AND status NOT IN ('deleted', 'deleting')`,
		orgID).Scan(&n)
	return n, err
}

// ListFreePerkOrgs returns the org IDs of every site currently holding the
// perk (cap checks during assignment flows that know the site, not the org).
func (s *Store) ListFreePerkOrgs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT organization_id FROM websites
		WHERE free_perk AND status NOT IN ('deleted', 'deleting')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func scanList(rows pgx.Rows) ([]Website, error) {
	var out []Website
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

var _ = errors.New
