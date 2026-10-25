package contractors

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

// Service is the application boundary for contractor management.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: NewRepository(), now: time.Now, auditf: audit.Record}
}

// Create registers an active contractor. The contact email is optional and is
// never an authenticated identity.
func (s *Service) Create(ctx context.Context, actor identity.Identity, orgID uuid.UUID, rawName, rawEmail string) (Contractor, error) {
	name, err := validate.Name(rawName)
	if err != nil {
		return Contractor{}, err
	}
	email, err := validate.Email(rawEmail)
	if err != nil {
		return Contractor{}, err
	}
	var created Contractor
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageContractors); err != nil {
			return err
		}
		var err error
		created, err = s.repo.Create(ctx, tx, orgID, name, email)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionContractorCreated, ResourceType: audit.ResourceContractor, ResourceID: created.ID,
			Details: map[string]any{"fields": []string{"name", "contactEmail"}},
		})
	})
	if err != nil {
		return Contractor{}, err
	}
	return created, nil
}

// Get returns a tenant-scoped contractor. Any active member.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID, contractorID uuid.UUID) (Contractor, error) {
	var c Contractor
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewContractors); err != nil {
			return err
		}
		var err error
		c, err = s.repo.Get(ctx, tx, orgID, contractorID)
		return err
	})
	if err != nil {
		return Contractor{}, err
	}
	return c, nil
}

// List returns tenant-scoped contractors in stable keyset order.
func (s *Service) List(ctx context.Context, actor identity.Identity, orgID uuid.UUID, f Filter, limit int, rawCursor string) ([]Contractor, string, error) {
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
	var items []Contractor
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewContractors); err != nil {
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

// Update applies an explicitly supplied name and/or contact email. Omitting a
// field preserves it; contactEmail null clears it.
func (s *Service) Update(ctx context.Context, actor identity.Identity, orgID, contractorID uuid.UUID, patch Patch) (Contractor, error) {
	patch, err := patch.NormalizePatch()
	if err != nil {
		return Contractor{}, err
	}
	var updated Contractor
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageContractors); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, contractorID)
		if err != nil {
			return err
		}
		changed := make([]string, 0, 2)
		details := map[string]any{}
		var name *string
		if patch.Name.Set && *patch.Name.Value != current.Name {
			name = patch.Name.Value
			changed = append(changed, "name")
			details["previous_name"] = current.Name
			details["name"] = *name
		}
		emailSet := false
		if patch.ContactEmail.Set && !sameEmail(current.ContactEmail, patch.ContactEmail.Value) {
			emailSet = true
			changed = append(changed, "contactEmail")
		}
		if len(changed) == 0 {
			updated = current
			return nil
		}
		updated, err = s.repo.Update(ctx, tx, orgID, contractorID, name, emailSet, patch.ContactEmail.Value)
		if err != nil {
			return err
		}
		details["fields"] = changed
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionContractorUpdated, ResourceType: audit.ResourceContractor, ResourceID: contractorID,
			Details: details,
		})
	})
	if err != nil {
		return Contractor{}, err
	}
	return updated, nil
}

func sameEmail(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Deactivate prevents new engagements. Repeats are an idempotent success.
func (s *Service) Deactivate(ctx context.Context, actor identity.Identity, orgID, contractorID uuid.UUID) (Contractor, error) {
	return s.setActive(ctx, actor, orgID, contractorID, false)
}

// Reactivate restores eligibility for future engagements.
func (s *Service) Reactivate(ctx context.Context, actor identity.Identity, orgID, contractorID uuid.UUID) (Contractor, error) {
	return s.setActive(ctx, actor, orgID, contractorID, true)
}

func (s *Service) setActive(ctx context.Context, actor identity.Identity, orgID, contractorID uuid.UUID, active bool) (Contractor, error) {
	var result Contractor
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageContractors); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, contractorID)
		if err != nil {
			return err
		}
		if current.IsActive == active {
			result = current
			return nil
		}
		if !active {
			blocking, err := s.repo.CountBlockingEngagements(ctx, tx, orgID, contractorID, validate.Today(s.now()))
			if err != nil {
				return err
			}
			if blocking > 0 {
				return apperr.ErrDeactivationBlocked
			}
		}
		result, err = s.repo.SetActive(ctx, tx, orgID, contractorID, active)
		if err != nil {
			return err
		}
		action := audit.ActionContractorDeactivated
		if active {
			action = audit.ActionContractorReactivated
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: action, ResourceType: audit.ResourceContractor, ResourceID: contractorID,
		})
	})
	if err != nil {
		return Contractor{}, err
	}
	return result, nil
}
