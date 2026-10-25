package sites

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
	"vendorproof/internal/validate"
)

// Repository owns site SQL. Every tenant statement filters by organization_id.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const columns = `id, organization_id, name, is_active, created_at, updated_at`

func scan(row pgx.Row) (Site, error) {
	var s Site
	err := row.Scan(&s.ID, &s.OrganizationID, &s.Name, &s.IsActive, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, apperr.ErrNotFound
	}
	if err != nil {
		return Site{}, fmt.Errorf("scan site: %w", err)
	}
	return s, nil
}

func (r *Repository) Create(ctx context.Context, q database.Querier, orgID uuid.UUID, name string) (Site, error) {
	s, err := scan(q.QueryRow(ctx, `
		INSERT INTO sites (id, organization_id, name)
		VALUES ($1, $2, $3)
		RETURNING `+columns, uuid.New(), orgID, name))
	if err != nil {
		return Site{}, fmt.Errorf("create site: %w", err)
	}
	return s, nil
}

func (r *Repository) Get(ctx context.Context, q database.Querier, orgID, siteID uuid.UUID) (Site, error) {
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM sites WHERE organization_id = $1 AND id = $2`, orgID, siteID))
}

func (r *Repository) List(ctx context.Context, q database.Querier, orgID uuid.UUID, f Filter, limit int, cur *pagination.Cursor) ([]Site, error) {
	afterAt, afterID := cur.Args()
	rows, err := q.Query(ctx, `
		SELECT `+columns+`
		FROM sites
		WHERE organization_id = $1
		  AND ($2::boolean IS NULL OR is_active = $2)
		  AND ($3::text = '' OR name ILIKE $3 ESCAPE '\')
		  AND ($4::timestamptz IS NULL OR (created_at, id) > ($4, $5::uuid))
		ORDER BY created_at, id
		LIMIT $6`,
		orgID, f.Active, validate.LikePattern(f.Search), afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) UpdateName(ctx context.Context, q database.Querier, orgID, siteID uuid.UUID, name string) (Site, error) {
	s, err := scan(q.QueryRow(ctx, `
		UPDATE sites SET name = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2
		RETURNING `+columns, orgID, siteID, name))
	if err != nil {
		return Site{}, fmt.Errorf("update site: %w", err)
	}
	return s, nil
}

func (r *Repository) SetActive(ctx context.Context, q database.Querier, orgID, siteID uuid.UUID, active bool) (Site, error) {
	s, err := scan(q.QueryRow(ctx, `
		UPDATE sites SET is_active = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2
		RETURNING `+columns, orgID, siteID, active))
	if err != nil {
		return Site{}, fmt.Errorf("set site active: %w", err)
	}
	return s, nil
}

// CountBlockingEngagements counts engagements still covering the deactivation
// date or any later date. active_until is exclusive, so an engagement ending
// today does not block.
func (r *Repository) CountBlockingEngagements(ctx context.Context, q database.Querier, orgID, siteID uuid.UUID, today time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM engagements
		WHERE organization_id = $1 AND site_id = $2
		  AND (active_until IS NULL OR active_until > $3::date)`, orgID, siteID, today).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count site engagements: %w", err)
	}
	return n, nil
}
