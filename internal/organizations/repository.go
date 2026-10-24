package organizations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/authz"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
)

// Repository owns every SQL statement for organizations and memberships.
// Tenant-owned statements always filter by organization_id.
type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Pool exposes the pool for read-only paths.
func (r *Repository) Pool() *pgxpool.Pool { return r.pool }

func (r *Repository) ListOrganizations(ctx context.Context, q database.Querier, ident identity.Identity, limit int, cur *cursor) ([]OrganizationWithRole, error) {
	afterAt, afterID := cur.args()
	rows, err := q.Query(ctx, `
		SELECT m.id, m.created_at, o.id, o.name, o.created_at, o.updated_at, m.role
		FROM memberships m
		JOIN organizations o ON o.id = m.organization_id
		WHERE m.identity_issuer = $1 AND m.identity_subject = $2 AND m.revoked_at IS NULL
		  AND ($3::timestamptz IS NULL OR (m.created_at, m.id) > ($3, $4::uuid))
		ORDER BY m.created_at, m.id
		LIMIT $5`,
		ident.Issuer, ident.Subject, afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	var out []OrganizationWithRole
	for rows.Next() {
		var item OrganizationWithRole
		if err := rows.Scan(&item.CursorID, &item.CursorAt, &item.ID, &item.Name, &item.CreatedAt, &item.UpdatedAt, &item.Role); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) ListMembershipsForIdentity(ctx context.Context, q database.Querier, ident identity.Identity, limit int, cur *cursor) ([]MembershipWithOrganization, error) {
	afterAt, afterID := cur.args()
	rows, err := q.Query(ctx, `
		SELECT m.id, m.organization_id, m.identity_issuer, m.identity_subject, m.role,
		       m.created_at, m.updated_at, o.name
		FROM memberships m
		JOIN organizations o ON o.id = m.organization_id
		WHERE m.identity_issuer = $1 AND m.identity_subject = $2 AND m.revoked_at IS NULL
		  AND ($3::timestamptz IS NULL OR (m.created_at, m.id) > ($3, $4::uuid))
		ORDER BY m.created_at, m.id
		LIMIT $5`,
		ident.Issuer, ident.Subject, afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list memberships for identity: %w", err)
	}
	defer rows.Close()

	var out []MembershipWithOrganization
	for rows.Next() {
		var item MembershipWithOrganization
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.Identity.Issuer, &item.Identity.Subject,
			&item.Role, &item.CreatedAt, &item.UpdatedAt, &item.OrganizationName); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetOrganizationForMember returns the organization only if the identity has an
// active membership; the role comes from the same row.
func (r *Repository) GetOrganizationForMember(ctx context.Context, q database.Querier, orgID uuid.UUID, ident identity.Identity) (Organization, authz.Role, error) {
	var org Organization
	var role authz.Role
	err := q.QueryRow(ctx, `
		SELECT o.id, o.name, o.created_at, o.updated_at, m.role
		FROM organizations o
		JOIN memberships m ON m.organization_id = o.id
		WHERE o.id = $1 AND m.identity_issuer = $2 AND m.identity_subject = $3
		  AND m.revoked_at IS NULL`,
		orgID, ident.Issuer, ident.Subject,
	).Scan(&org.ID, &org.Name, &org.CreatedAt, &org.UpdatedAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, "", ErrNotFound
	}
	if err != nil {
		return Organization{}, "", fmt.Errorf("get organization: %w", err)
	}
	return org, role, nil
}

// GetActiveMembership resolves the caller's role within an organization.
func (r *Repository) GetActiveMembership(ctx context.Context, q database.Querier, orgID uuid.UUID, ident identity.Identity) (Membership, error) {
	var m Membership
	err := q.QueryRow(ctx, `
		SELECT id, organization_id, identity_issuer, identity_subject, role, created_at, updated_at, revoked_at
		FROM memberships
		WHERE organization_id = $1 AND identity_issuer = $2 AND identity_subject = $3
		  AND revoked_at IS NULL`,
		orgID, ident.Issuer, ident.Subject,
	).Scan(&m.ID, &m.OrganizationID, &m.Identity.Issuer, &m.Identity.Subject, &m.Role,
		&m.CreatedAt, &m.UpdatedAt, &m.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	if err != nil {
		return Membership{}, fmt.Errorf("get membership: %w", err)
	}
	return m, nil
}

// GetMembershipIncludingRevoked scopes by organization so a membership UUID
// from another tenant is indistinguishable from a missing one.
func (r *Repository) GetMembershipIncludingRevoked(ctx context.Context, q database.Querier, orgID, membershipID uuid.UUID) (Membership, error) {
	var m Membership
	err := q.QueryRow(ctx, `
		SELECT id, organization_id, identity_issuer, identity_subject, role, created_at, updated_at, revoked_at
		FROM memberships
		WHERE organization_id = $1 AND id = $2`,
		orgID, membershipID,
	).Scan(&m.ID, &m.OrganizationID, &m.Identity.Issuer, &m.Identity.Subject, &m.Role,
		&m.CreatedAt, &m.UpdatedAt, &m.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	if err != nil {
		return Membership{}, fmt.Errorf("get membership by id: %w", err)
	}
	return m, nil
}

func (r *Repository) ListActiveMemberships(ctx context.Context, q database.Querier, orgID uuid.UUID, limit int, cur *cursor) ([]Membership, error) {
	afterAt, afterID := cur.args()
	rows, err := q.Query(ctx, `
		SELECT id, organization_id, identity_issuer, identity_subject, role, created_at, updated_at, revoked_at
		FROM memberships
		WHERE organization_id = $1 AND revoked_at IS NULL
		  AND ($2::timestamptz IS NULL OR (created_at, id) > ($2, $3::uuid))
		ORDER BY created_at, id
		LIMIT $4`,
		orgID, afterAt, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()

	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.OrganizationID, &m.Identity.Issuer, &m.Identity.Subject,
			&m.Role, &m.CreatedAt, &m.UpdatedAt, &m.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LockOrganization serializes membership mutations for one organization for
// the duration of the transaction, making last-owner checks race-free.
func (r *Repository) LockOrganization(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, orgID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	return nil
}

func (r *Repository) CountActiveOwners(ctx context.Context, q database.Querier, orgID uuid.UUID) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM memberships
		WHERE organization_id = $1 AND role = 'owner' AND revoked_at IS NULL`, orgID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count owners: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateOrganization(ctx context.Context, q database.Querier, id uuid.UUID, name string) (Organization, error) {
	var org Organization
	err := q.QueryRow(ctx, `
		INSERT INTO organizations (id, name)
		VALUES ($1, $2)
		RETURNING id, name, created_at, updated_at`, id, name,
	).Scan(&org.ID, &org.Name, &org.CreatedAt, &org.UpdatedAt)
	if err != nil {
		return Organization{}, fmt.Errorf("create organization: %w", err)
	}
	return org, nil
}

func (r *Repository) UpdateOrganizationName(ctx context.Context, q database.Querier, orgID uuid.UUID, name string) (Organization, error) {
	var org Organization
	err := q.QueryRow(ctx, `
		UPDATE organizations
		SET name = $2, updated_at = now()
		WHERE id = $1
		RETURNING id, name, created_at, updated_at`, orgID, name,
	).Scan(&org.ID, &org.Name, &org.CreatedAt, &org.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, ErrNotFound
	}
	if err != nil {
		return Organization{}, fmt.Errorf("update organization: %w", err)
	}
	return org, nil
}

const activeMembershipUnique = "memberships_active_identity_unique"

func (r *Repository) CreateMembership(ctx context.Context, q database.Querier, m Membership) (Membership, error) {
	m.ID = uuid.New()
	err := q.QueryRow(ctx, `
		INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`,
		m.ID, m.OrganizationID, m.Identity.Issuer, m.Identity.Subject, m.Role,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
	if database.IsUniqueViolation(err, activeMembershipUnique) {
		return Membership{}, ErrConflict
	}
	if err != nil {
		return Membership{}, fmt.Errorf("create membership: %w", err)
	}
	return m, nil
}

func (r *Repository) UpdateMembershipRole(ctx context.Context, q database.Querier, orgID, membershipID uuid.UUID, role authz.Role) error {
	tag, err := q.Exec(ctx, `
		UPDATE memberships SET role = $3, updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND revoked_at IS NULL`,
		orgID, membershipID, role,
	)
	if err != nil {
		return fmt.Errorf("update membership role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) RevokeMembership(ctx context.Context, q database.Querier, orgID, membershipID uuid.UUID) error {
	tag, err := q.Exec(ctx, `
		UPDATE memberships SET revoked_at = now(), updated_at = now()
		WHERE organization_id = $1 AND id = $2 AND revoked_at IS NULL`,
		orgID, membershipID,
	)
	if err != nil {
		return fmt.Errorf("revoke membership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
