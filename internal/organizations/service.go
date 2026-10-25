// Package organizations owns organizations, memberships, and their invariants.
// Services perform authorization, transaction boundaries, and audit writes;
// handlers only translate HTTP.
package organizations

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/audit"
	"vendorproof/internal/authz"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/orgaccess"
)

// Service is the application boundary for organization administration.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
	// afterAuthorize is a test seam for deterministic authorization races.
	afterAuthorize func()
}

func NewService(pool *pgxpool.Pool, repo *Repository) *Service {
	return &Service{pool: pool, repo: repo, now: time.Now, auditf: audit.Record}
}

// List returns only organizations where the caller has an active membership.
func (s *Service) List(ctx context.Context, actor identity.Identity, limit int, rawCursor string) ([]OrganizationWithRole, string, error) {
	limit, rawCursor, err := Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	cur, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}
	items, err := s.repo.ListOrganizations(ctx, s.pool, actor, limit, cur)
	if err != nil {
		return nil, "", err
	}
	if len(items) == limit {
		last := items[len(items)-1]
		return items, nextCursor(last.CursorAt, last.CursorID), nil
	}
	return items, "", nil
}

// Me lists the caller's active memberships, bounded by the same pagination.
func (s *Service) Me(ctx context.Context, actor identity.Identity, limit int, rawCursor string) ([]MembershipWithOrganization, string, error) {
	limit, rawCursor, err := Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	cur, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}
	items, err := s.repo.ListMembershipsForIdentity(ctx, s.pool, actor, limit, cur)
	if err != nil {
		return nil, "", err
	}
	if len(items) == limit {
		last := items[len(items)-1]
		return items, nextCursor(last.CreatedAt, last.ID), nil
	}
	return items, "", nil
}

// Get returns tenant-protected organization details.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID uuid.UUID) (Organization, authz.Role, error) {
	return s.repo.GetOrganizationForMember(ctx, s.pool, orgID, actor)
}

// Update changes an organization name. Owner only.
func (s *Service) Update(ctx context.Context, actor identity.Identity, orgID uuid.UUID, rawName string) (Organization, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return Organization{}, err
	}
	var out Organization
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Same lock/order as membership mutation: an owner revocation that
		// commits during this request cannot slip between authorization and
		// the organization update.
		if err := s.repo.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, tx, orgID, actor, authz.CanUpdateOrganization); err != nil {
			return err
		}
		org, err := s.repo.UpdateOrganizationName(ctx, tx, orgID, name)
		if err != nil {
			return err
		}
		out = org
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID,
			Actor:          actor,
			Action:         audit.ActionOrganizationUpdated,
			ResourceType:   audit.ResourceOrganization,
			ResourceID:     orgID,
			Details:        map[string]any{"fields": []string{"name"}},
		})
	})
	if err != nil {
		return Organization{}, err
	}
	return out, nil
}

// ListMemberships returns active memberships. Owner only.
func (s *Service) ListMemberships(ctx context.Context, actor identity.Identity, orgID uuid.UUID, limit int, rawCursor string) ([]Membership, string, error) {
	limit, rawCursor, err := Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	cur, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}
	var items []Membership
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, orgID, actor, authz.CanManageMemberships); err != nil {
			return err
		}
		if s.afterAuthorize != nil {
			s.afterAuthorize()
		}
		var err error
		items, err = s.repo.ListActiveMemberships(ctx, tx, orgID, limit, cur)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if len(items) == limit {
		last := items[len(items)-1]
		return items, nextCursor(last.CreatedAt, last.ID), nil
	}
	return items, "", nil
}

// AddMembershipByInput validates an administrative issuer/subject invitation.
func (s *Service) AddMembershipByInput(ctx context.Context, actor identity.Identity, orgID uuid.UUID, issuer, subject, rawRole string) (Membership, error) {
	member, err := NormalizeIdentity(issuer, subject)
	if err != nil {
		return Membership{}, err
	}
	role, err := ParseRole(rawRole)
	if err != nil {
		return Membership{}, err
	}
	return s.AddMembership(ctx, actor, orgID, member, role)
}

// AddMembership creates an owner-authorized membership.
func (s *Service) AddMembership(ctx context.Context, actor identity.Identity, orgID uuid.UUID, member identity.Identity, role authz.Role) (Membership, error) {
	if !member.Valid() {
		return Membership{}, validationErr("identity", "issuer and subject are required")
	}
	if !role.Valid() {
		return Membership{}, validationErr("role", "must be owner, manager, or reviewer")
	}
	var created Membership
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, tx, orgID, actor, authz.CanManageMemberships); err != nil {
			return err
		}
		var err error
		created, err = s.repo.CreateMembership(ctx, tx, Membership{
			OrganizationID: orgID,
			Identity:       member,
			Role:           role,
		})
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID,
			Actor:          actor,
			Action:         audit.ActionMembershipCreated,
			ResourceType:   audit.ResourceMembership,
			ResourceID:     created.ID,
			Details:        map[string]any{"issuer": member.Issuer, "subject": member.Subject, "role": role},
		})
	})
	if err != nil {
		return Membership{}, err
	}
	return created, nil
}

// ChangeMembershipRole changes a role while preserving at least one owner.
func (s *Service) ChangeMembershipRole(ctx context.Context, actor identity.Identity, orgID, membershipID uuid.UUID, rawRole string) (Membership, error) {
	role, err := ParseRole(rawRole)
	if err != nil {
		return Membership{}, err
	}
	var result Membership
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, tx, orgID, actor, authz.CanManageMemberships); err != nil {
			return err
		}
		target, err := s.repo.GetMembershipIncludingRevoked(ctx, tx, orgID, membershipID)
		if err != nil {
			return err
		}
		if target.RevokedAt != nil {
			return ErrConflict
		}
		if target.Role == role {
			result = target
			return nil
		}
		fromRole := target.Role
		if target.Role == authz.Owner {
			owners, err := s.repo.CountActiveOwners(ctx, tx, orgID)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwner
			}
		}
		if err := s.repo.UpdateMembershipRole(ctx, tx, orgID, membershipID, role); err != nil {
			return err
		}
		target.Role = role
		target.UpdatedAt = s.now().UTC()
		result = target
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID,
			Actor:          actor,
			Action:         audit.ActionMembershipRoleChanged,
			ResourceType:   audit.ResourceMembership,
			ResourceID:     membershipID,
			Details:        map[string]any{"from": fromRole, "to": role},
		})
	})
	if err != nil {
		return Membership{}, err
	}
	return result, nil
}

// RevokeMembership soft-revokes a membership, preserving audit history.
func (s *Service) RevokeMembership(ctx context.Context, actor identity.Identity, orgID, membershipID uuid.UUID) error {
	return database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.repo.LockOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := s.authorize(ctx, tx, orgID, actor, authz.CanManageMemberships); err != nil {
			return err
		}
		target, err := s.repo.GetMembershipIncludingRevoked(ctx, tx, orgID, membershipID)
		if err != nil {
			return err
		}
		if target.RevokedAt != nil {
			return ErrConflict
		}
		if target.Role == authz.Owner {
			owners, err := s.repo.CountActiveOwners(ctx, tx, orgID)
			if err != nil {
				return err
			}
			if owners <= 1 {
				return ErrLastOwner
			}
		}
		if err := s.repo.RevokeMembership(ctx, tx, orgID, membershipID); err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID,
			Actor:          actor,
			Action:         audit.ActionMembershipRevoked,
			ResourceType:   audit.ResourceMembership,
			ResourceID:     membershipID,
			Details:        map[string]any{"role": target.Role, "subject": target.Identity.Subject},
		})
	})
}

// Bootstrap atomically creates an organization and its first owner. It is the
// only path that creates an organization in Phase 1 and is invoked by the
// local administrative CLI, not an unauthenticated route.
func (s *Service) Bootstrap(ctx context.Context, name, issuer, subject string) (Organization, Membership, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return Organization{}, Membership{}, err
	}
	owner, err := NormalizeIdentity(issuer, subject)
	if err != nil {
		return Organization{}, Membership{}, err
	}

	var org Organization
	var membership Membership
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		org, err = s.repo.CreateOrganization(ctx, tx, uuid.New(), normalized)
		if err != nil {
			return err
		}
		membership, err = s.repo.CreateMembership(ctx, tx, Membership{
			OrganizationID: org.ID,
			Identity:       owner,
			Role:           authz.Owner,
		})
		if err != nil {
			return err
		}
		if err := s.auditf(ctx, tx, audit.Event{
			OrganizationID: org.ID,
			Actor:          owner,
			Action:         audit.ActionOrganizationCreated,
			ResourceType:   audit.ResourceOrganization,
			ResourceID:     org.ID,
		}); err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: org.ID,
			Actor:          owner,
			Action:         audit.ActionOrganizationBootstrapped,
			ResourceType:   audit.ResourceOrganization,
			ResourceID:     org.ID,
			Details:        map[string]any{"source": "admin_cli", "owner_membership_id": membership.ID.String()},
		})
	})
	if err != nil {
		return Organization{}, Membership{}, err
	}
	return org, membership, nil
}

// authorize resolves the caller's active membership through the shared
// primitive. Missing membership is deliberately reported as not-found.
func (s *Service) authorize(ctx context.Context, q database.Querier, orgID uuid.UUID, actor identity.Identity, allowed func(authz.Role) bool) (orgaccess.Access, error) {
	return orgaccess.Authorize(ctx, q, orgID, actor, allowed)
}
