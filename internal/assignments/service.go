package assignments

import (
	"context"
	"fmt"
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

// exclusionConstraint for assignment overlaps.
const exclusionConstraint = "requirement_assignments_no_overlap"

// Service is the application boundary for requirement assignments.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repository
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, repo: NewRepository(), now: time.Now, auditf: audit.Record}
}

// Create assigns a requirement version to an engagement for a date range.
func (s *Service) Create(ctx context.Context, actor identity.Identity, orgID uuid.UUID, in CreateInput) (Assignment, error) {
	norm, err := in.Normalize()
	if err != nil {
		return Assignment{}, err
	}
	var created Assignment
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageAssignments); err != nil {
			return err
		}
		// Resolve the requirement family from the version.
		family, err := s.repo.GetVersionFamily(ctx, tx, orgID, norm.RequirementVersionID)
		if err != nil {
			return err
		}
		if family == uuid.Nil {
			return apperr.ErrNotFound
		}
		// Verify the engagement belongs to this org.
		if !s.engagementExists(ctx, tx, orgID, norm.EngagementID) {
			return apperr.ErrNotFound
		}
		created, err = s.repo.Create(ctx, tx, orgID, norm, family)
		if database.IsExclusionViolation(err, exclusionConstraint) {
			return apperr.ErrConflict
		}
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionRequirementAssignmentCreated, ResourceType: audit.ResourceAssignment, ResourceID: created.ID,
			Details: map[string]any{
				"engagement_id":          norm.EngagementID.String(),
				"requirement_version_id": norm.RequirementVersionID.String(),
				"effective_from":         norm.EffectiveFrom.Format("2006-01-02"),
				"effective_until":        formatDatePtr(norm.EffectiveUntil),
			},
		})
	})
	if err != nil {
		return Assignment{}, err
	}
	return created, nil
}

// engagementExists checks if an engagement belongs to the tenant.
func (s *Service) engagementExists(ctx context.Context, q database.Querier, orgID, engagementID uuid.UUID) bool {
	var exists bool
	_ = q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM engagements WHERE organization_id = $1 AND id = $2)`, orgID, engagementID).Scan(&exists)
	return exists
}

// End ends an assignment prospectively at the given date.
func (s *Service) End(ctx context.Context, actor identity.Identity, orgID, assignmentID uuid.UUID, rawUntil string) (Assignment, error) {
	until, err := validateDate("effectiveUntil", rawUntil)
	if err != nil {
		return Assignment{}, err
	}
	var ended Assignment
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageAssignments); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, assignmentID)
		if err != nil {
			return err
		}
		if current.ID == uuid.Nil {
			return apperr.ErrNotFound
		}
		if current.EffectiveUntil != nil && !current.EffectiveUntil.IsZero() {
			// Already ended; ending again is idempotent if same date.
			if current.EffectiveUntil.Equal(until) {
				ended = current
				return nil
			}
			return apperr.ErrConflict
		}
		if !until.After(current.EffectiveFrom) {
			return apperr.Validation("effectiveUntil", "must be later than effectiveFrom")
		}
		ended, err = s.repo.EndAssignment(ctx, tx, orgID, assignmentID, until)
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionRequirementAssignmentEnded, ResourceType: audit.ResourceAssignment, ResourceID: assignmentID,
			Details: map[string]any{
				"effective_until": until.Format("2006-01-02"),
			},
		})
	})
	if err != nil {
		return Assignment{}, err
	}
	return ended, nil
}

// Transition atomically changes an assignment to a newer requirement version
// prospectively. The old assignment is ended and a new one created for the
// same engagement/family with the new version.
func (s *Service) Transition(ctx context.Context, actor identity.Identity, orgID, assignmentID uuid.UUID, in TransitionInput) (Assignment, Assignment, error) {
	norm, err := in.Normalize()
	if err != nil {
		return Assignment{}, Assignment{}, err
	}
	var ended, created Assignment
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageAssignments); err != nil {
			return err
		}
		current, err := s.repo.Get(ctx, tx, orgID, assignmentID)
		if err != nil {
			return err
		}
		if current.ID == uuid.Nil {
			return apperr.ErrNotFound
		}
		// Resolve the new version's family.
		newFamily, err := s.repo.GetVersionFamily(ctx, tx, orgID, norm.RequirementVersionID)
		if err != nil {
			return err
		}
		if newFamily == uuid.Nil {
			return apperr.ErrNotFound
		}
		// Family must match the old assignment's family.
		if newFamily != current.RequirementKey {
			return apperr.Validation("requirementVersionId", "must belong to the same requirement family")
		}
		// Verify the new version's effective_from allows this transition.
		// The transition date must be >= both old effective_from and new version's effective_from.
		newVersionEffectiveFrom, err := s.versionEffectiveFrom(ctx, tx, orgID, norm.RequirementVersionID)
		if err != nil {
			return err
		}
		if norm.EffectiveFrom.Before(newVersionEffectiveFrom) {
			return apperr.Validation("effectiveFrom", "must not be earlier than the requirement version effective date")
		}
		if !norm.EffectiveFrom.After(current.EffectiveFrom) {
			return apperr.Validation("effectiveFrom", "must be later than the current assignment's effectiveFrom")
		}
		// End the old assignment at the transition date.
		ended, err = s.repo.EndAssignment(ctx, tx, orgID, assignmentID, norm.EffectiveFrom)
		if err != nil {
			return err
		}
		// Create the new assignment.
		created, err = s.repo.InsertNewTransition(ctx, tx, orgID, norm, newFamily)
		if database.IsExclusionViolation(err, exclusionConstraint) {
			return apperr.ErrConflict
		}
		if err != nil {
			return err
		}
		return s.auditf(ctx, tx, audit.Event{
			OrganizationID: orgID, Actor: actor,
			Action: audit.ActionRequirementAssignmentTransitioned, ResourceType: audit.ResourceAssignment, ResourceID: created.ID,
			Details: map[string]any{
				"previous_assignment_id": assignmentID.String(),
				"previous_version_id":    current.RequirementVersionID.String(),
				"new_version_id":         norm.RequirementVersionID.String(),
				"effective_from":         norm.EffectiveFrom.Format("2006-01-02"),
			},
		})
	})
	if err != nil {
		return Assignment{}, Assignment{}, err
	}
	return ended, created, nil
}

// versionEffectiveFrom returns the effective_from of a requirement version.
func (s *Service) versionEffectiveFrom(ctx context.Context, q database.Querier, orgID, versionID uuid.UUID) (time.Time, error) {
	var from time.Time
	err := q.QueryRow(ctx, `
		SELECT effective_from FROM requirement_versions WHERE organization_id = $1 AND id = $2`,
		orgID, versionID).Scan(&from)
	if err != nil {
		return time.Time{}, fmt.Errorf("load version effective from: %w", err)
	}
	return from, nil
}

// Get retrieves a single assignment.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID, assignmentID uuid.UUID) (Assignment, error) {
	var a Assignment
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewAssignments); err != nil {
			return err
		}
		var err error
		a, err = s.repo.Get(ctx, tx, orgID, assignmentID)
		return err
	})
	if err != nil {
		return Assignment{}, err
	}
	if a.ID == uuid.Nil {
		return Assignment{}, apperr.ErrNotFound
	}
	return a, nil
}

// List returns assignments with optional filters.
func (s *Service) List(ctx context.Context, actor identity.Identity, orgID uuid.UUID, f Filter, limit int, rawCursor string) ([]Assignment, string, error) {
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, "", err
	}
	limit, rawCursor, err = pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, "", err
	}
	var items []Assignment
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewAssignments); err != nil {
			return err
		}
		var err error
		items, err = s.repo.List(ctx, tx, orgID, f, limit, cur)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = pagination.Encode(last.AssignedAt, last.ID)
	}
	return items, next, nil
}

func validateDate(field, raw string) (time.Time, error) {
	return validate.Date(field, raw)
}

func formatDatePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format("2006-01-02")
}
