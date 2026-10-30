package requirements

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/apperr"
	"vendorproof/internal/audit"
	"vendorproof/internal/authz"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/orgaccess"
	"vendorproof/internal/pagination"
)

// Service is the application boundary for requirement management.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: NewRepository(), now: time.Now, auditf: audit.Record}
}

// Create creates a requirement family and its first immutable version atomically.
func (s *Service) Create(ctx context.Context, actor identity.Identity, orgID uuid.UUID, in CreateInput) (RequirementVersion, error) {
	np, err := in.PublishInput().Normalize()
	if err != nil {
		return RequirementVersion{}, err
	}
	var created RequirementVersion
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageRequirements); err != nil {
			return err
		}
		var err error
		created, err = s.repo.CreateFamilyAndVersion(ctx, tx, orgID, np)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionRequirementCreated, ResourceType: audit.ResourceRequirement, ResourceID: created.RequirementKey,
			Details: map[string]any{
				"requirement_key": created.RequirementKey.String(),
				"version":         1,
				"name":            created.Name,
			},
		})
	})
	if err != nil {
		return RequirementVersion{}, err
	}
	return created, nil
}

// (CreateInput).PublishInput converts to PublishInput.
func (in CreateInput) PublishInput() PublishInput {
	return PublishInput{
		Name:              in.Name,
		Cadence:           in.Cadence,
		DueDay:            in.DueDay,
		EffectiveFrom:     in.EffectiveFrom,
		EffectiveUntil:    in.EffectiveUntil,
		ReportingTimezone: in.ReportingTimezone,
	}
}

// PublishVersion creates a new immutable version of an existing family.
// Concurrent publications use database uniqueness as the final authority.
func (s *Service) PublishVersionByKey(ctx context.Context, actor identity.Identity, orgID, key uuid.UUID, in PublishInput) (RequirementVersion, error) {
	np, err := in.Normalize()
	if err != nil {
		return RequirementVersion{}, err
	}
	var created RequirementVersion
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageRequirements); err != nil {
			return err
		}
		exists, err := s.repo.FamilyExists(ctx, tx, orgID, key)
		if err != nil {
			return err
		}
		if !exists {
			return apperr.ErrNotFound
		}
		versionNum, err := s.repo.NextVersionNumber(ctx, tx, orgID, key)
		if err != nil {
			return err
		}
		created, err = s.repo.PublishVersion(ctx, tx, orgID, key, versionNum, np)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionRequirementVersionPublished, ResourceType: audit.ResourceRequirement, ResourceID: created.ID,
			Details: map[string]any{
				"requirement_key": key.String(),
				"version":         versionNum,
				"name":            created.Name,
			},
		})
	})
	if err != nil {
		return RequirementVersion{}, err
	}
	return created, nil
}

// ListFamilies returns all requirement families for a tenant.
func (s *Service) ListFamilies(ctx context.Context, actor identity.Identity, orgID uuid.UUID, limit int, rawCursor string) ([]Requirement, string, error) {
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, "", err
	}
	limit, rawCursor, err = pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	var items []Requirement
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewRequirements); err != nil {
			return err
		}
		var err error
		items, err = s.repo.ListFamilies(ctx, tx, orgID, limit, cur)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = pagination.Encode(last.CreatedAt, last.ID)
	}
	return items, next, nil
}

// ListVersions returns all versions of a family (newest first).
func (s *Service) ListVersions(ctx context.Context, actor identity.Identity, orgID, key uuid.UUID, limit int, rawCursor string) ([]RequirementVersion, string, error) {
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, "", err
	}
	limit, rawCursor, err = pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	var items []RequirementVersion
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewRequirements); err != nil {
			return err
		}
		var err error
		items, err = s.repo.ListVersions(ctx, tx, orgID, key, limit, cur)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = pagination.Encode(last.CreatedAt, last.ID)
	}
	return items, next, nil
}

// GetVersion retrieves an exact version by ID.
func (s *Service) GetVersion(ctx context.Context, actor identity.Identity, orgID, versionID uuid.UUID) (RequirementVersion, error) {
	var rv RequirementVersion
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewRequirements); err != nil {
			return err
		}
		var err error
		rv, err = s.repo.GetVersion(ctx, tx, orgID, versionID)
		return err
	})
	if err != nil {
		return RequirementVersion{}, err
	}
	return rv, nil
}
