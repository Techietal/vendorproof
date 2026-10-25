// Package orgaccess is the shared primitive for organization-scoped
// authorization and lock ordering. Every Phase 2 write starts by locking the
// organization row and then resolving the caller's active membership inside the
// same transaction, so a revocation that commits mid-request cannot be
// outrun by a stale permission check.
package orgaccess

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
)

// Access is the resolved active membership.
type Access struct {
	MembershipID uuid.UUID
	Role         authz.Role
}

// LockOrganization serializes writes for one tenant. The organization row is
// the single lock every Phase 2 write path takes first, which establishes a
// total order and avoids deadlocks between entities.
func LockOrganization(ctx context.Context, q database.Querier, orgID uuid.UUID) error {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, orgID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	return nil
}

// Authorize resolves the caller's active membership and applies a role check.
// Missing membership (including another tenant's organization) is not-found.
func Authorize(ctx context.Context, q database.Querier, orgID uuid.UUID, actor identity.Identity, allowed func(authz.Role) bool) (Access, error) {
	var access Access
	err := q.QueryRow(ctx, `
		SELECT id, role FROM memberships
		WHERE organization_id = $1 AND identity_issuer = $2 AND identity_subject = $3
		  AND revoked_at IS NULL`,
		orgID, actor.Issuer, actor.Subject,
	).Scan(&access.MembershipID, &access.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Access{}, apperr.ErrNotFound
	}
	if err != nil {
		return Access{}, fmt.Errorf("authorize organization access: %w", err)
	}
	if !allowed(access.Role) {
		return Access{}, apperr.ErrForbidden
	}
	return access, nil
}

// LockAndAuthorize is the standard write-path preamble.
func LockAndAuthorize(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, actor identity.Identity, allowed func(authz.Role) bool) (Access, error) {
	if err := LockOrganization(ctx, tx, orgID); err != nil {
		return Access{}, err
	}
	return Authorize(ctx, tx, orgID, actor, allowed)
}
