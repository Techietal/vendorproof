package engagements

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"vendorproof/internal/apperr"
	"vendorproof/internal/database"
	"vendorproof/internal/pagination"
)

// Repository owns engagement SQL. Every statement is tenant scoped.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const columns = `id, organization_id, contractor_id, site_id, active_from, active_until, created_at, updated_at`

func scan(row pgx.Row) (Engagement, error) {
	var e Engagement
	err := row.Scan(&e.ID, &e.OrganizationID, &e.ContractorID, &e.SiteID,
		&e.ActiveFrom, &e.ActiveUntil, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Engagement{}, apperr.ErrNotFound
	}
	if err != nil {
		return Engagement{}, fmt.Errorf("scan engagement: %w", err)
	}
	e.ActiveFrom = e.ActiveFrom.UTC()
	if e.ActiveUntil != nil {
		u := e.ActiveUntil.UTC()
		e.ActiveUntil = &u
	}
	return e, nil
}

func (r *Repository) Create(ctx context.Context, q database.Querier, orgID uuid.UUID, c Creation) (Engagement, error) {
	e, err := scan(q.QueryRow(ctx, `
		INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from, active_until)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+columns,
		uuid.New(), orgID, c.ContractorID, c.SiteID, c.ActiveFrom, c.ActiveUntil))
	if err != nil {
		return Engagement{}, fmt.Errorf("create engagement: %w", err)
	}
	return e, nil
}

func (r *Repository) Get(ctx context.Context, q database.Querier, orgID, engagementID uuid.UUID) (Engagement, error) {
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM engagements WHERE organization_id = $1 AND id = $2`, orgID, engagementID))
}

// List returns engagements with their state for the evaluation date.
func (r *Repository) List(ctx context.Context, q database.Querier, orgID uuid.UUID, f Filter, limit int, cur *pagination.Cursor) ([]Engagement, []State, error) {
	afterAt, afterID := cur.Args()
	state := ""
	if f.State != nil {
		state = string(*f.State)
	}
	rows, err := q.Query(ctx, `
		SELECT `+columns+`,
		       CASE WHEN $3::date < active_from THEN 'scheduled'
		            WHEN active_until IS NOT NULL AND $3::date >= active_until THEN 'ended'
		            ELSE 'active' END AS state
		FROM engagements
		WHERE organization_id = $1
		  AND ($2::uuid IS NULL OR contractor_id = $2)
		  AND ($4::uuid IS NULL OR site_id = $4)
		  AND ($5::text = '' OR CASE WHEN $3::date < active_from THEN 'scheduled'
		            WHEN active_until IS NOT NULL AND $3::date >= active_until THEN 'ended'
		            ELSE 'active' END = $5)
		  AND ($6::timestamptz IS NULL OR (created_at, id) > ($6, $7::uuid))
		ORDER BY created_at, id
		LIMIT $8`,
		orgID, f.ContractorID, f.AsOf, f.SiteID, state, afterAt, afterID, limit,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list engagements: %w", err)
	}
	defer rows.Close()
	var out []Engagement
	var states []State
	for rows.Next() {
		var e Engagement
		var st State
		if err := rows.Scan(&e.ID, &e.OrganizationID, &e.ContractorID, &e.SiteID,
			&e.ActiveFrom, &e.ActiveUntil, &e.CreatedAt, &e.UpdatedAt, &st); err != nil {
			return nil, nil, fmt.Errorf("scan engagement: %w", err)
		}
		out = append(out, e)
		states = append(states, st)
	}
	return out, states, rows.Err()
}

// UpdatePeriod writes a corrected or closed period.
func (r *Repository) UpdatePeriod(ctx context.Context, q database.Querier, orgID, engagementID uuid.UUID, from time.Time, until *time.Time) (Engagement, error) {
	e, err := scan(q.QueryRow(ctx, `
		UPDATE engagements SET active_from = $3, active_until = $4, updated_at = now()
		WHERE organization_id = $1 AND id = $2
		RETURNING `+columns, orgID, engagementID, from, until))
	if err != nil {
		return Engagement{}, fmt.Errorf("update engagement: %w", err)
	}
	return e, nil
}

// CountOverlaps counts other engagements for the same pair whose half-open
// range intersects. The exclusion constraint is the final authority; this
// produces an application conflict without waiting for the insert.
func (r *Repository) CountOverlaps(ctx context.Context, q database.Querier, orgID, contractorID, siteID uuid.UUID, from time.Time, until *time.Time, excludeID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM engagements
		WHERE organization_id = $1 AND contractor_id = $2 AND site_id = $3
		  AND ($6::uuid IS NULL OR id <> $6)
		  AND daterange(active_from, active_until, '[)') && daterange($4::date, $5::date, '[)')`,
		orgID, contractorID, siteID, from, until, nullableUUID(excludeID)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count engagement overlaps: %w", err)
	}
	return n, nil
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// ReferenceCounts reports downstream references used by correction guards.
func (r *Repository) ReferenceCounts(ctx context.Context, q database.Querier, orgID, engagementID uuid.UUID) (assignments, obligations int, err error) {
	err = q.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM requirement_assignments WHERE organization_id = $1 AND engagement_id = $2),
		       (SELECT count(*) FROM obligations WHERE organization_id = $1 AND engagement_id = $2)`,
		orgID, engagementID).Scan(&assignments, &obligations)
	if err != nil {
		return 0, 0, fmt.Errorf("count engagement references: %w", err)
	}
	return assignments, obligations, nil
}

// CoverageViolations counts obligations whose period is not fully inside the
// proposed interval.
func (r *Repository) CoverageViolations(ctx context.Context, q database.Querier, orgID, engagementID uuid.UUID, from time.Time, until *time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM obligations
		WHERE organization_id = $1 AND engagement_id = $2
		  AND (period_start < $3::date OR ($4::date IS NOT NULL AND period_end > $4::date))`,
		orgID, engagementID, from, until).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("check obligation coverage: %w", err)
	}
	return n, nil
}

// Parents verifies both parents belong to the tenant and loads their status.
func (r *Repository) Parents(ctx context.Context, q database.Querier, orgID, contractorID, siteID uuid.UUID) (contractorFound, contractorActive, siteFound, siteActive bool, err error) {
	err = q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM contractors WHERE organization_id = $1 AND id = $2),
		       COALESCE((SELECT is_active FROM contractors WHERE organization_id = $1 AND id = $2), false),
		       EXISTS (SELECT 1 FROM sites WHERE organization_id = $1 AND id = $3),
		       COALESCE((SELECT is_active FROM sites WHERE organization_id = $1 AND id = $3), false)`,
		orgID, contractorID, siteID).Scan(&contractorFound, &contractorActive, &siteFound, &siteActive)
	if err != nil {
		return false, false, false, false, fmt.Errorf("load engagement parents: %w", err)
	}
	return contractorFound, contractorActive, siteFound, siteActive, nil
}
