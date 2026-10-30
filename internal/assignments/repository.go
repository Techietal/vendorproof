package assignments

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

// Repository owns assignment SQL. Every statement is tenant scoped.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

const assignmentColumns = `id, organization_id, engagement_id, requirement_key,
	requirement_version_id, effective_from, effective_until, assigned_at`

func scan(row pgx.Row) (Assignment, error) {
	var a Assignment
	var effUntil *time.Time
	err := row.Scan(&a.ID, &a.OrganizationID, &a.EngagementID, &a.RequirementKey,
		&a.RequirementVersionID, &a.EffectiveFrom, &effUntil, &a.AssignedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Assignment{}, nil
	}
	if err != nil {
		return Assignment{}, fmt.Errorf("scan assignment: %w", err)
	}
	a.EffectiveFrom = a.EffectiveFrom.UTC()
	a.EffectiveUntil = effUntil
	return a, nil
}

// Create inserts a new assignment.
func (r *Repository) Create(ctx context.Context, q database.Querier, orgID uuid.UUID, in NormalizedCreate, versionKey uuid.UUID) (Assignment, error) {
	var a Assignment
	err := q.QueryRow(ctx, `
		INSERT INTO requirement_assignments
			(id, organization_id, engagement_id, requirement_key, requirement_version_id,
			 effective_from, effective_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+assignmentColumns,
		uuid.New(), orgID, in.EngagementID, versionKey, in.RequirementVersionID,
		in.EffectiveFrom, in.EffectiveUntil,
	).Scan(&a.ID, &a.OrganizationID, &a.EngagementID, &a.RequirementKey,
		&a.RequirementVersionID, &a.EffectiveFrom, &a.EffectiveUntil, &a.AssignedAt)
	if err != nil {
		return Assignment{}, fmt.Errorf("create assignment: %w", err)
	}
	return a, nil
}

// Get retrieves an assignment by ID.
func (r *Repository) Get(ctx context.Context, q database.Querier, orgID, assignmentID uuid.UUID) (Assignment, error) {
	row := q.QueryRow(ctx, `SELECT `+assignmentColumns+` FROM requirement_assignments WHERE organization_id = $1 AND id = $2`, orgID, assignmentID)
	a, err := scan(row)
	if err != nil {
		return Assignment{}, err
	}
	if a.ID == uuid.Nil {
		return Assignment{}, nil
	}
	return a, nil
}

// List returns assignments in stable keyset order.
func (r *Repository) List(ctx context.Context, q database.Querier, orgID uuid.UUID, f Filter, limit int, cur *pagination.Cursor) ([]Assignment, error) {
	afterAt, afterID := cur.Args()
	rows, err := q.Query(ctx, `
		SELECT `+assignmentColumns+`
		FROM requirement_assignments
		WHERE organization_id = $1
		  AND ($2::uuid IS NULL OR engagement_id = $2)
		  AND ($3::uuid IS NULL OR requirement_key = $3)
		  AND ($4::timestamptz IS NULL OR (assigned_at, id) > ($4, $5::uuid))
		ORDER BY assigned_at, id
		LIMIT $6`, orgID, f.EngagementID, f.RequirementKey, afterAt, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if a.ID == uuid.Nil {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// EndAssignment sets effective_until to the given date (exclusive end).
func (r *Repository) EndAssignment(ctx context.Context, q database.Querier, orgID, assignmentID uuid.UUID, until time.Time) (Assignment, error) {
	var a Assignment
	err := q.QueryRow(ctx, `
		UPDATE requirement_assignments
		SET effective_until = $3
		WHERE organization_id = $1 AND id = $2
		RETURNING `+assignmentColumns, orgID, assignmentID, until,
	).Scan(&a.ID, &a.OrganizationID, &a.EngagementID, &a.RequirementKey,
		&a.RequirementVersionID, &a.EffectiveFrom, &a.EffectiveUntil, &a.AssignedAt)
	if err != nil {
		return Assignment{}, fmt.Errorf("end assignment: %w", err)
	}
	return a, nil
}

// TransitionAssignment creates a new assignment row for a version transition.
// The old assignment is ended at the transition's effective_from.
func (r *Repository) InsertTransition(ctx context.Context, q database.Querier, orgID uuid.UUID, oldID uuid.UUID, in NormalizedTransition, versionKey uuid.UUID) (Assignment, error) {
	var a Assignment
	err := q.QueryRow(ctx, `
		UPDATE requirement_assignments
		SET effective_until = $3
		WHERE organization_id = $1 AND id = $2
		RETURNING `+assignmentColumns, orgID, oldID, in.EffectiveFrom,
	).Scan(&a.ID, &a.OrganizationID, &a.EngagementID, &a.RequirementKey,
		&a.RequirementVersionID, &a.EffectiveFrom, &a.EffectiveUntil, &a.AssignedAt)
	if err != nil {
		return Assignment{}, fmt.Errorf("end old assignment: %w", err)
	}
	return a, nil
}

// InsertNewTransition adds the new assignment row.
func (r *Repository) InsertNewTransition(ctx context.Context, q database.Querier, orgID uuid.UUID, in NormalizedTransition, versionKey uuid.UUID) (Assignment, error) {
	var a Assignment
	err := q.QueryRow(ctx, `
		INSERT INTO requirement_assignments
			(id, organization_id, engagement_id, requirement_key, requirement_version_id,
			 effective_from, effective_until)
		VALUES ($1, $2, (SELECT engagement_id FROM requirement_assignments WHERE organization_id = $2 AND id = $4),
				$5, $6, $7, NULL)
		RETURNING `+assignmentColumns,
		uuid.New(), orgID, in.RequirementVersionID, in.EffectiveFrom, versionKey, in.EffectiveFrom,
	).Scan(&a.ID, &a.OrganizationID, &a.EngagementID, &a.RequirementKey,
		&a.RequirementVersionID, &a.EffectiveFrom, &a.EffectiveUntil, &a.AssignedAt)
	if err != nil {
		return Assignment{}, fmt.Errorf("insert transition assignment: %w", err)
	}
	return a, nil
}

// GetVersionByKeyAndNumber returns a requirement version within a family.
func (r *Repository) GetVersionNumber(ctx context.Context, q database.Querier, orgID, key uuid.UUID, versionNum int) (uuid.UUID, error) {
	var versionID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT id FROM requirement_versions WHERE organization_id = $1 AND requirement_key = $2 AND version = $3`,
		orgID, key, versionNum).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get version by number: %w", err)
	}
	return versionID, nil
}

// GetVersionFamily returns the requirement_key for a version.
func (r *Repository) GetVersionFamily(ctx context.Context, q database.Querier, orgID, versionID uuid.UUID) (uuid.UUID, error) {
	var key uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT requirement_key FROM requirement_versions WHERE organization_id = $1 AND id = $2`,
		orgID, versionID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get version family: %w", err)
	}
	return key, nil
}
