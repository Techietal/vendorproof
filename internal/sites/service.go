package sites

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
	"vendorproof/internal/validate"
)

// Service is the application boundary for site management.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: NewRepository(), now: time.Now, auditf: audit.Record}
}

// Create registers an active site. Owner or manager.
func (s *Service) Create(ctx context.Context, actor identity.Identity, orgID uuid.UUID, rawName string) (Site, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return Site{}, err
	}
	var created Site
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageSites); err != nil {
			return err
		}
		var err error
		created, err = s.repo.Create(ctx, tx, orgID, name)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionSiteCreated, ResourceType: audit.ResourceSite, ResourceID: created.ID,
			Details: map[string]any{"fields": []string{"name"}},
		})
	})
	if err != nil {
		return Site{}, err
	}
	return created, nil
}

// Get returns a tenant-scoped site. Any active member.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID, siteID uuid.UUID) (Site, error) {
	var site Site
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewSites); err != nil {
			return err
		}
		var err error
		site, err = s.repo.Get(ctx, tx, orgID, siteID)
		return err
	})
	if err != nil {
		return Site{}, err
	}
	return site, nil
}

// List returns tenant-scoped sites in stable keyset order.
func (s *Service) List(ctx context.Context, actor identity.Identity, orgID uuid.UUID, f Filter, limit int, rawCursor string) ([]Site, string, error) {
	if err := f.Normalize(); err != nil {
		return nil, "", err
	}
	limit, rawCursor, err := pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, "", err
	}
	var items []Site
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewSites); err != nil {
			return err
		}
		var err error
		items, err = s.repo.List(ctx, tx, orgID, f, limit, cur)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if len(items) == limit {
		last := items[len(items)-1]
		return items, pagination.Encode(last.CreatedAt, last.ID), nil
	}
	return items, "", nil
}

// Update changes a site name. Owner or manager.
func (s *Service) Update(ctx context.Context, actor identity.Identity, orgID, siteID uuid.UUID, rawName *string) (Site, error) {
	name, err := ValidateNameField(rawName)
	if err != nil {
		return Site{}, err
	}
	var updated Site
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageSites); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, siteID)
		if err != nil {
			return err
		}
		if current.Name == name {
			updated = current
			return nil
		}
		updated, err = s.repo.UpdateName(ctx, tx, orgID, siteID, name)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionSiteUpdated, ResourceType: audit.ResourceSite, ResourceID: siteID,
			Details: map[string]any{"fields": []string{"name"}, "previous_name": current.Name, "name": name},
		})
	})
	if err != nil {
		return Site{}, err
	}
	return updated, nil
}

// Deactivate prevents new engagements. Repeating the same transition is an
// idempotent success and writes no audit event.
func (s *Service) Deactivate(ctx context.Context, actor identity.Identity, orgID, siteID uuid.UUID) (Site, error) {
	return s.setActive(ctx, actor, orgID, siteID, false)
}

// Reactivate restores eligibility for new engagements without reviving old
// engagements.
func (s *Service) Reactivate(ctx context.Context, actor identity.Identity, orgID, siteID uuid.UUID) (Site, error) {
	return s.setActive(ctx, actor, orgID, siteID, true)
}

func (s *Service) setActive(ctx context.Context, actor identity.Identity, orgID, siteID uuid.UUID, active bool) (Site, error) {
	var result Site
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageSites); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, siteID)
		if err != nil {
			return err
		}
		if current.IsActive == active {
			result = current
			return nil
		}
		if !active {
			blocking, err := s.repo.CountBlockingEngagements(ctx, tx, orgID, siteID, validate.Today(s.now()))
			if err != nil {
				return err
			}
			if blocking > 0 {
				return apperr.ErrDeactivationBlocked
			}
		}
		result, err = s.repo.SetActive(ctx, tx, orgID, siteID, active)
		if err != nil {
			return err
		}
		action := audit.ActionSiteDeactivated
		if active {
			action = audit.ActionSiteReactivated
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: action, ResourceType: audit.ResourceSite, ResourceID: siteID,
		})
	})
	if err != nil {
		return Site{}, err
	}
	return result, nil
}
