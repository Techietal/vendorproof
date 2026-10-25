package engagements

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

const exclusionConstraint = "engagements_no_overlap"

// Service is the application boundary for engagement history.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: NewRepository(), now: time.Now, auditf: audit.Record}
}

// Create registers a new engagement. Both parents must be active members of
// the organization and the period must not overlap the same pair.
func (s *Service) Create(ctx context.Context, actor identity.Identity, orgID uuid.UUID, in Creation) (Engagement, error) {
	if in.ContractorID == uuid.Nil {
		return Engagement{}, apperr.Validation("contractorId", "must be a UUID")
	}
	if in.SiteID == uuid.Nil {
		return Engagement{}, apperr.Validation("siteId", "must be a UUID")
	}
	if err := ValidatePeriod(in.ActiveFrom, in.ActiveUntil); err != nil {
		return Engagement{}, err
	}
	var created Engagement
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageEngagements); err != nil {
			return err
		}
		cFound, cActive, sFound, sActive, err := s.repo.Parents(ctx, tx, orgID, in.ContractorID, in.SiteID)
		if err != nil {
			return err
		}
		if !cFound || !sFound {
			return apperr.ErrNotFound
		}
		if !cActive {
			return apperr.ErrInactiveContractor
		}
		if !sActive {
			return apperr.ErrInactiveSite
		}
		if err := s.checkOverlap(ctx, tx, orgID, in.ContractorID, in.SiteID, in.ActiveFrom, in.ActiveUntil, uuid.Nil); err != nil {
			return err
		}
		created, err = s.repo.Create(ctx, tx, orgID, in)
		if database.IsExclusionViolation(err, exclusionConstraint) {
			return apperr.ErrConflict
		}
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionEngagementCreated, ResourceType: audit.ResourceEngagement, ResourceID: created.ID,
			Details: map[string]any{
				"contractor_id": in.ContractorID.String(),
				"site_id":       in.SiteID.String(),
				"active_from":   FormatDate(in.ActiveFrom),
				"active_until":  optionalDate(in.ActiveUntil),
			},
		})
	})
	if err != nil {
		return Engagement{}, err
	}
	return created, nil
}

// Get returns one tenant-scoped engagement.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID, engagementID uuid.UUID) (Engagement, error) {
	var e Engagement
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewEngagements); err != nil {
			return err
		}
		var err error
		e, err = s.repo.Get(ctx, tx, orgID, engagementID)
		return err
	})
	if err != nil {
		return Engagement{}, err
	}
	return e, nil
}

// List returns engagements and their states for an evaluation date. The
// evaluation date defaults to the server UTC calendar date.
func (s *Service) List(ctx context.Context, actor identity.Identity, orgID uuid.UUID, f Filter, limit int, rawCursor string) ([]Engagement, []State, time.Time, string, error) {
	if f.AsOf.IsZero() {
		f.AsOf = validate.Today(s.now())
	}
	limit, rawCursor, err := pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, nil, time.Time{}, "", err
	}
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, nil, time.Time{}, "", err
	}
	var items []Engagement
	var states []State
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewEngagements); err != nil {
			return err
		}
		var err error
		items, states, err = s.repo.List(ctx, tx, orgID, f, limit, cur)
		return err
	})
	if err != nil {
		return nil, nil, time.Time{}, "", err
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = pagination.Encode(last.CreatedAt, last.ID)
	}
	return items, states, f.AsOf, next, nil
}

// Correct applies a narrow date correction. Downstream references guard the
// change so existing evidence relationships keep their meaning.
func (s *Service) Correct(ctx context.Context, actor identity.Identity, orgID, engagementID uuid.UUID, patch Patch) (Engagement, error) {
	patch, err := patch.NormalizePatch()
	if err != nil {
		return Engagement{}, err
	}
	var updated Engagement
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageEngagements); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, engagementID)
		if err != nil {
			return err
		}
		from, until, err := applyPatch(current, patch)
		if err != nil {
			return err
		}
		if from.Equal(current.ActiveFrom) && sameDate(until, current.ActiveUntil) {
			updated = current
			return nil
		}
		if err := ValidatePeriod(from, until); err != nil {
			return err
		}
		if err := s.guard(ctx, tx, orgID, current, from, until); err != nil {
			return err
		}
		if err := s.checkOverlap(ctx, tx, orgID, current.ContractorID, current.SiteID, from, until, engagementID); err != nil {
			return err
		}
		updated, err = s.repo.UpdatePeriod(ctx, tx, orgID, engagementID, from, until)
		if database.IsExclusionViolation(err, exclusionConstraint) {
			return apperr.ErrConflict
		}
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionEngagementCorrected, ResourceType: audit.ResourceEngagement, ResourceID: engagementID,
			Details: map[string]any{
				"fields":                changedDates(current, from, until),
				"previous_active_from":  FormatDate(current.ActiveFrom),
				"previous_active_until": optionalDate(current.ActiveUntil),
				"active_from":           FormatDate(from),
				"active_until":          optionalDate(until),
			},
		})
	})
	if err != nil {
		return Engagement{}, err
	}
	return updated, nil
}

// Close sets an exclusive end date. Repeating the same date is idempotent;
// another date on an already closed engagement is a conflict resolved through
// Correct.
func (s *Service) Close(ctx context.Context, actor identity.Identity, orgID, engagementID uuid.UUID, rawUntil string) (Engagement, error) {
	until, err := validate.Date("activeUntil", rawUntil)
	if err != nil {
		return Engagement{}, err
	}
	var result Engagement
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageEngagements); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, engagementID)
		if err != nil {
			return err
		}
		if !until.After(current.ActiveFrom) {
			return apperr.Validation("activeUntil", "must be later than activeFrom")
		}
		if current.ActiveUntil != nil {
			if current.ActiveUntil.Equal(until) {
				result = current
				return nil
			}
			return apperr.ErrConflict
		}
		if err := s.guard(ctx, tx, orgID, current, current.ActiveFrom, &until); err != nil {
			return err
		}
		if err := s.checkOverlap(ctx, tx, orgID, current.ContractorID, current.SiteID, current.ActiveFrom, &until, engagementID); err != nil {
			return err
		}
		result, err = s.repo.UpdatePeriod(ctx, tx, orgID, engagementID, current.ActiveFrom, &until)
		if database.IsExclusionViolation(err, exclusionConstraint) {
			return apperr.ErrConflict
		}
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionEngagementClosed, ResourceType: audit.ResourceEngagement, ResourceID: engagementID,
			Details: map[string]any{
				"active_from":           FormatDate(current.ActiveFrom),
				"previous_active_until": optionalDate(nil),
				"active_until":          FormatDate(until),
			},
		})
	})
	if err != nil {
		return Engagement{}, err
	}
	return result, nil
}

func (s *Service) checkOverlap(ctx context.Context, q database.Querier, orgID, contractorID, siteID uuid.UUID, from time.Time, until *time.Time, excludeID uuid.UUID) error {
	n, err := s.repo.CountOverlaps(ctx, q, orgID, contractorID, siteID, from, until, excludeID)
	if err != nil {
		return err
	}
	if n > 0 {
		return apperr.ErrConflict
	}
	return nil
}

// guard rejects changes that would invalidate downstream references.
func (s *Service) guard(ctx context.Context, q database.Querier, orgID uuid.UUID, current Engagement, from time.Time, until *time.Time) error {
	assignments, obligations, err := s.repo.ReferenceCounts(ctx, q, orgID, current.ID)
	if err != nil {
		return err
	}
	if assignments == 0 && obligations == 0 {
		return nil
	}
	if obligations > 0 {
		violations, err := s.repo.CoverageViolations(ctx, q, orgID, current.ID, from, until)
		if err != nil {
			return err
		}
		if violations > 0 {
			return apperr.ErrImmutable
		}
	}
	if assignments > 0 && obligations == 0 {
		// Assigned requirements have no date window of their own, so only
		// extensions are provably safe without Phase 3 semantics.
		if from.After(current.ActiveFrom) {
			return apperr.ErrImmutable
		}
		if until != nil && (current.ActiveUntil == nil || until.Before(*current.ActiveUntil)) {
			return apperr.ErrImmutable
		}
	}
	return nil
}

func applyPatch(current Engagement, p Patch) (time.Time, *time.Time, error) {
	from := current.ActiveFrom
	until := current.ActiveUntil
	if p.ActiveFrom.Set {
		parsed, err := validate.Date("activeFrom", *p.ActiveFrom.Value)
		if err != nil {
			return time.Time{}, nil, err
		}
		from = parsed
	}
	if p.ActiveUntil.Set {
		if p.ActiveUntil.Value == nil {
			until = nil
		} else {
			parsed, err := validate.Date("activeUntil", *p.ActiveUntil.Value)
			if err != nil {
				return time.Time{}, nil, err
			}
			until = &parsed
		}
	}
	return from, until, nil
}

func sameDate(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func changedDates(current Engagement, from time.Time, until *time.Time) []string {
	fields := []string{}
	if !from.Equal(current.ActiveFrom) {
		fields = append(fields, "activeFrom")
	}
	if !sameDate(until, current.ActiveUntil) {
		fields = append(fields, "activeUntil")
	}
	return fields
}

func optionalDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return FormatDate(*t)
}
