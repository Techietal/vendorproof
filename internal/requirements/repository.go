package requirements

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"vendorproof/internal/database"
	"vendorproof/internal/pagination"
)

// Repository owns requirement SQL. Every statement is tenant scoped.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const versionColumns = `id, organization_id, requirement_key, version, name, cadence,
	due_day, effective_from, effective_until, reporting_timezone, created_at`

// ScanRequirementVersion scans a full version row.
func scanVersion(row pgx.Row) (RequirementVersion, error) {
	var rv RequirementVersion
	var dueDay *int
	var effectiveUntil *[]byte
	err := row.Scan(
		&rv.ID, &rv.OrganizationID, &rv.RequirementKey, &rv.Version, &rv.Name,
		&rv.Cadence, &dueDay, &rv.EffectiveFrom, &effectiveUntil, &rv.ReportingTimezone, &rv.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequirementVersion{}, nil
	}
	if err != nil {
		return RequirementVersion{}, fmt.Errorf("scan requirement version: %w", err)
	}
	if dueDay != nil {
		rv.DueDay = *dueDay
	}
	rv.EffectiveFrom = rv.EffectiveFrom.UTC()
	if effectiveUntil != nil && *effectiveUntil != nil {
		t := parseDateBytes(*effectiveUntil)
		rv.EffectiveUntil = &t
	}
	return rv, nil
}

func parseDateBytes(b []byte) time.Time {
	// PostgreSQL date comes back as a formatted string; parse to UTC date.
	s := string(b)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return time.Time{}
}

// CreateFamily creates a new requirement family atomically with its first version.
func (r *Repository) CreateFamilyAndVersion(ctx context.Context, q database.Querier, orgID uuid.UUID, in NormalizedPublish) (RequirementVersion, error) {
	key := uuid.New()
	var rv RequirementVersion
	err := q.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO requirements (id, organization_id)
			VALUES ($1, $2)
			RETURNING id
		)
		INSERT INTO requirement_versions
			(id, organization_id, requirement_key, version, name, cadence, due_day,
			 effective_from, effective_until, reporting_timezone)
		SELECT $3, $2, $1, 1, $4, $5, $6, $7, $8, $9 FROM inserted
		RETURNING `+versionColumns,
		key, orgID, uuid.New(), in.Name, string(in.Cadence),
		func() *int {
			if in.Cadence == CadenceMonthly {
				return &in.DueDay
			}
			return nil
		}(),
		normalizeDate(in.EffectiveFrom), normalizeUntil(in.EffectiveUntil), in.ReportingTimezone,
	).Scan(&rv.ID, &rv.OrganizationID, &rv.RequirementKey, &rv.Version, &rv.Name,
		&rv.Cadence, new(int), &rv.EffectiveFrom, &rv.EffectiveUntil, &rv.ReportingTimezone, &rv.CreatedAt)
	if err != nil {
		return RequirementVersion{}, fmt.Errorf("create requirement family: %w", err)
	}
	return rv, nil
}

// NextVersionNumber returns the next version number for a family.
func (r *Repository) NextVersionNumber(ctx context.Context, q database.Querier, orgID, key uuid.UUID) (int, error) {
	var next int
	err := q.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1 FROM requirement_versions
		WHERE organization_id = $1 AND requirement_key = $2`, orgID, key).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("next version number: %w", err)
	}
	return next, nil
}

// PublishVersion inserts a new immutable version row.
func (r *Repository) PublishVersion(ctx context.Context, q database.Querier, orgID, key uuid.UUID, versionNum int, in NormalizedPublish) (RequirementVersion, error) {
	var rv RequirementVersion
	var dueDay *int
	if in.Cadence == CadenceMonthly {
		dueDay = &in.DueDay
	}
	err := q.QueryRow(ctx, `
		INSERT INTO requirement_versions
			(id, organization_id, requirement_key, version, name, cadence, due_day,
			 effective_from, effective_until, reporting_timezone)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+versionColumns,
		uuid.New(), orgID, key, versionNum, in.Name, string(in.Cadence),
		dueDay, normalizeDate(in.EffectiveFrom), normalizeUntil(in.EffectiveUntil), in.ReportingTimezone,
	).Scan(&rv.ID, &rv.OrganizationID, &rv.RequirementKey, &rv.Version, &rv.Name,
		&rv.Cadence, new(int), &rv.EffectiveFrom, &rv.EffectiveUntil, &rv.ReportingTimezone, &rv.CreatedAt)
	if err != nil {
		return RequirementVersion{}, fmt.Errorf("publish version: %w", err)
	}
	return rv, nil
}

// FamilyExists checks if a requirement family exists for the tenant.
func (r *Repository) FamilyExists(ctx context.Context, q database.Querier, orgID, key uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM requirements WHERE organization_id = $1 AND id = $2)`, orgID, key).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check family existence: %w", err)
	}
	return exists, nil
}

// ListFamilies returns all requirement families for a tenant.
func (r *Repository) ListFamilies(ctx context.Context, q database.Querier, orgID uuid.UUID, limit int, cur *pagination.Cursor) ([]Requirement, error) {
	afterAt, afterID := cur.Args()
	rows, err := q.Query(ctx, `
		SELECT id, organization_id, created_at
		FROM requirements
		WHERE organization_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3::uuid))
		ORDER BY created_at, id
		LIMIT $4`, orgID, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list families: %w", err)
	}
	defer rows.Close()
	var out []Requirement
	for rows.Next() {
		var req Requirement
		if err := rows.Scan(&req.ID, &req.OrganizationID, &req.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan family: %w", err)
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// ListVersions returns all versions of a family, newest first.
func (r *Repository) ListVersions(ctx context.Context, q database.Querier, orgID, key uuid.UUID, limit int, cur *pagination.Cursor) ([]RequirementVersion, error) {
	afterAt, afterID := cur.Args()
	rows, err := q.Query(ctx, `
		SELECT `+versionColumns+`
		FROM requirement_versions
		WHERE organization_id = $1 AND requirement_key = $2
		  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $5`, orgID, key, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer rows.Close()
	var out []RequirementVersion
	for rows.Next() {
		rv, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		if rv.ID == uuid.Nil {
			continue
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}

// GetVersion returns a specific version by ID.
func (r *Repository) GetVersion(ctx context.Context, q database.Querier, orgID, versionID uuid.UUID) (RequirementVersion, error) {
	row := q.QueryRow(ctx, `SELECT `+versionColumns+` FROM requirement_versions WHERE organization_id = $1 AND id = $2`, orgID, versionID)
	rv, err := scanVersion(row)
	if err != nil {
		return RequirementVersion{}, err
	}
	if rv.ID == uuid.Nil {
		return RequirementVersion{}, err
	}
	return rv, nil
}

// GetFamilyByVersion looks up the family for a given version.
func (r *Repository) GetFamilyByVersion(ctx context.Context, q database.Querier, orgID, versionID uuid.UUID) (uuid.UUID, error) {
	var key uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT requirement_key FROM requirement_versions WHERE organization_id = $1 AND id = $2`, orgID, versionID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get family by version: %w", err)
	}
	return key, nil
}

func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func normalizeUntil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := normalizeDate(*t)
	return &u
}
