package contractors

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

// Repository owns contractor SQL. Every tenant statement filters by
// organization_id.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const columns = `id, organization_id, name, contact_email, is_active, created_at, updated_at`

func scan(row pgx.Row) (Contractor, error) {
	var c Contractor
	err := row.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.ContactEmail, &c.IsActive, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Contractor{}, apperr.ErrNotFound
	}
	if err != nil {
		return Contractor{}, fmt.Errorf("scan contractor: %w", err)
	}
	return c, nil
}

func (r *Repository) Create(ctx context.Context, q database.Querier, orgID uuid.UUID, name string, email *string) (Contractor, error) {
	c, err := scan(q.QueryRow(ctx, `
		INSERT INTO contractors (id, organization_id, name, contact_email)
		VALUES ($1, $2, $3, $4)
		RETURNING `+columns, uuid.New(), orgID, name, email))
	if err != nil {
		return Contractor{}, fmt.Errorf("create contractor: %w", err)
	}
	return c, nil
}

func (r *Repository) Get(ctx context.Context, q database.Querier, orgID, contractorID uuid.UUID) (Contractor, error) {
	return scan(q.QueryRow(ctx, `SELECT `+columns+` FROM contractors WHERE organization_id = $1 AND id = $2`, orgID, contractorID))
}

func (r *Repository) List(ctx context.Context, q database.Querier, orgID uuid.UUID, f Filter, limit int, cur *pagination.Cursor) ([]Contractor, error) {
	afterAt, afterID := cur.Args()
	rows, err := q.Query(ctx, `
		SELECT `+columns+`
		FROM contractors
		WHERE organization_id = $1
		  AND ($2::boolean IS NULL OR is_active = $2)
		  AND ($3::text = '' OR name ILIKE $3 ESCAPE '\')
		  AND ($4::timestamptz IS NULL OR (created_at, id) > ($4, $5::uuid))
		ORDER BY created_at, id
		LIMIT $6`,
		orgID, f.Active, validate.LikePattern(f.Search), afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list contractors: %w", err)
	}
	defer rows.Close()
	var out []Contractor
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update writes only the supplied columns.
func (r *Repository) Update(ctx context.Context, q database.Querier, orgID, contractorID uuid.UUID, name *string, emailSet bool, email *string) (Contractor, error) {
	c, err := scan(q.QueryRow(ctx, `
		UPDATE contractors SET
			name = COALESCE($3, name),
			contact_email = CASE WHEN $4 THEN $5 ELSE contact_email END,
			updated_at = now()
		WHERE organization_id = $1 AND id = $2
		RETURNING `+columns, orgID, contractorID, name, emailSet, email))
	if err != nil {
		return Contractor{}, fmt.Errorf("update contractor: %w", err)
	}
	return c, nil
}

func (r *Repository) SetActive(ctx context.Context, q database.Querier, orgID, contractorID uuid.UUID, active bool) (Contractor, error) {
	c, err := scan(q.QueryRow(ctx, `
		UPDATE contractors SET is_active = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2
		RETURNING `+columns, orgID, contractorID, active))
	if err != nil {
		return Contractor{}, fmt.Errorf("set contractor active: %w", err)
	}
	return c, nil
}

// CountBlockingEngagements counts engagements still covering the deactivation
// date or later.
func (r *Repository) CountBlockingEngagements(ctx context.Context, q database.Querier, orgID, contractorID uuid.UUID, today time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM engagements
		WHERE organization_id = $1 AND contractor_id = $2
		  AND (active_until IS NULL OR active_until > $3::date)`, orgID, contractorID, today).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count contractor engagements: %w", err)
	}
	return n, nil
}
