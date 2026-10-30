package obligations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"vendorproof/internal/database"
	"vendorproof/internal/pagination"
)

// Repository owns obligation SQL. Every statement is tenant scoped.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const obligationColumns = `id, organization_id, engagement_id, requirement_key, requirement_version_id,
	period_start, period_end, coverage_start, coverage_end, due_at, state, created_at`

func scanObligation(row pgx.Row) (Obligation, error) {
	var ob Obligation
	var state string
	err := row.Scan(
		&ob.ID, &ob.OrganizationID, &ob.EngagementID, &ob.RequirementKey, &ob.RequirementVersionID,
		&ob.PeriodStart, &ob.PeriodEnd, &ob.CoverageStart, &ob.CoverageEnd,
		&ob.DueAt, &state, &ob.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Obligation{}, nil
	}
	if err != nil {
		return Obligation{}, fmt.Errorf("scan obligation: %w", err)
	}
	ob.State = ReviewState(state)
	ob.PeriodStart = ob.PeriodStart.UTC()
	ob.PeriodEnd = ob.PeriodEnd.UTC()
	ob.CoverageStart = ob.CoverageStart.UTC()
	ob.CoverageEnd = ob.CoverageEnd.UTC()
	ob.DueAt = ob.DueAt.UTC()
	ob.CreatedAt = ob.CreatedAt.UTC()
	return ob, nil
}

// Get retrieves a single obligation by ID.
func (r *Repository) Get(ctx context.Context, q database.Querier, orgID, obligationID uuid.UUID) (Obligation, error) {
	row := q.QueryRow(ctx, `SELECT `+obligationColumns+` FROM obligations WHERE organization_id = $1 AND id = $2`, orgID, obligationID)
	ob, err := scanObligation(row)
	if err != nil {
		return Obligation{}, err
	}
	return ob, nil
}

// List returns obligations matching the filter in stable keyset order.
func (r *Repository) List(ctx context.Context, q database.Querier, orgID uuid.UUID, f ListFilter, limit int, cur *pagination.Cursor) ([]Obligation, []DisplayStatus, error) {
	afterAt, afterID := cur.Args()

	query := `
		SELECT o.id, o.organization_id, o.engagement_id, o.requirement_key, o.requirement_version_id,
		       o.period_start, o.period_end, o.coverage_start, o.coverage_end, o.due_at, o.state, o.created_at,
		       e.contractor_id, e.site_id
		FROM obligations o
		JOIN engagements e ON e.id = o.engagement_id AND e.organization_id = o.organization_id
		WHERE o.organization_id = $1`

	args := []any{orgID}
	argPos := 2

	if f.ContractorID != nil {
		query += fmt.Sprintf(" AND e.contractor_id = $%d", argPos)
		args = append(args, *f.ContractorID)
		argPos++
	}
	if f.SiteID != nil {
		query += fmt.Sprintf(" AND e.site_id = $%d", argPos)
		args = append(args, *f.SiteID)
		argPos++
	}
	if f.EngagementID != nil {
		query += fmt.Sprintf(" AND o.engagement_id = $%d", argPos)
		args = append(args, *f.EngagementID)
		argPos++
	}
	if f.RequirementKey != nil {
		query += fmt.Sprintf(" AND o.requirement_key = $%d", argPos)
		args = append(args, *f.RequirementKey)
		argPos++
	}
	if f.RequirementVersionID != nil {
		query += fmt.Sprintf(" AND o.requirement_version_id = $%d", argPos)
		args = append(args, *f.RequirementVersionID)
		argPos++
	}
	if f.PeriodStart != nil {
		query += fmt.Sprintf(" AND o.period_start = $%d::date", argPos)
		args = append(args, *f.PeriodStart+"-01")
		argPos++
	}
	if f.DueAfter != nil {
		query += fmt.Sprintf(" AND o.due_at >= $%d::timestamptz", argPos)
		args = append(args, *f.DueAfter)
		argPos++
	}
	if f.DueBefore != nil {
		query += fmt.Sprintf(" AND o.due_at < $%d::timestamptz", argPos)
		args = append(args, *f.DueBefore)
		argPos++
	}
	if f.State != nil {
		query += fmt.Sprintf(" AND o.state = $%d", argPos)
		args = append(args, string(*f.State))
		argPos++
	}
	if f.OverdueOnly {
		query += fmt.Sprintf(" AND o.state IN ('awaiting_submission', 'needs_correction') AND o.due_at < $%d", argPos)
		args = append(args, f.AsOf)
		argPos++
	}

	query += fmt.Sprintf(" AND ($%d::timestamptz IS NULL OR (o.created_at, o.id) > ($%d, $%d::uuid))", argPos, argPos, argPos+1)
	args = append(args, afterAt, afterID)
	argPos += 2

	query += fmt.Sprintf(" ORDER BY o.created_at, o.id LIMIT $%d", argPos)
	args = append(args, limit)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list obligations: %w", err)
	}
	defer rows.Close()

	var out []Obligation
	var statuses []DisplayStatus
	for rows.Next() {
		var ob Obligation
		var state string
		var contractorID, siteID uuid.UUID
		err := rows.Scan(
			&ob.ID, &ob.OrganizationID, &ob.EngagementID, &ob.RequirementKey, &ob.RequirementVersionID,
			&ob.PeriodStart, &ob.PeriodEnd, &ob.CoverageStart, &ob.CoverageEnd,
			&ob.DueAt, &state, &ob.CreatedAt,
			&contractorID, &siteID,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("scan obligation: %w", err)
		}
		ob.State = ReviewState(state)
		ob.PeriodStart = ob.PeriodStart.UTC()
		ob.PeriodEnd = ob.PeriodEnd.UTC()
		ob.CoverageStart = ob.CoverageStart.UTC()
		ob.CoverageEnd = ob.CoverageEnd.UTC()
		ob.DueAt = ob.DueAt.UTC()
		ob.CreatedAt = ob.CreatedAt.UTC()

		status, _ := Status(ob.State, ob.DueAt, f.AsOf)
		out = append(out, ob)
		statuses = append(statuses, status)
	}
	return out, statuses, rows.Err()
}

// LatestSubmissionState returns the state of the latest submission attempt for an obligation.
func (r *Repository) LatestSubmissionState(ctx context.Context, q database.Querier, orgID, obligationID uuid.UUID) (*ReviewState, error) {
	var state string
	err := q.QueryRow(ctx, `
		SELECT s.state FROM submissions s
		WHERE s.organization_id = $1 AND s.obligation_id = $2
		ORDER BY s.attempt DESC, s.submitted_at DESC
		LIMIT 1`, orgID, obligationID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest submission state: %w", err)
	}
	rs := ReviewState(state)
	return &rs, nil
}
