package obligations

import (
	"context"
	"errors"
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
)

// Service is the application boundary for obligation generation and queries.
type Service struct {
	pool      *pgxpool.Pool
	repo      *Repository
	now       func() time.Time
	auditf    func(ctx context.Context, q database.Querier, e audit.Event) error
	generator *Generator
}

func NewService(pool *pgxpool.Pool) *Service {
	s := &Service{pool: pool, now: time.Now, auditf: audit.Record}
	s.repo = NewRepository()
	s.generator = NewGenerator(pool, s.auditGenerate)
	return s
}

func (s *Service) auditGenerate(ctx context.Context, q database.Querier, e audit.Event) error {
	if s.auditf == nil {
		return nil
	}
	// Set system actor for CLI generation.
	sysEvent := audit.Event{
		OrganizationID: e.OrganizationID,
		Actor:          identity.Identity{Issuer: "system", Subject: "cli"},
		Action:         e.Action,
		ResourceType:   e.ResourceType,
		ResourceID:     e.ResourceID,
		Details:        e.Details,
	}
	return s.auditf(ctx, q, sysEvent)
}

// GenerateForMonth generates monthly obligations for an organization and month.
func (s *Service) GenerateForMonth(ctx context.Context, actor identity.Identity, orgID uuid.UUID, year, month int, dryRun bool) (GenerationResult, error) {
	var result GenerationResult
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanGenerateObligations); err != nil {
			return err
		}
		var genErr error
		result, genErr = s.generator.GenerateForMonth(ctx, tx, orgID, year, month, dryRun)
		return genErr
	})
	if err != nil {
		return GenerationResult{}, err
	}
	return result, nil
}

// ListObligations returns obligations matching the filter.
func (s *Service) List(ctx context.Context, actor identity.Identity, orgID uuid.UUID, f ListFilter, limit int, rawCursor string) ([]Obligation, []DisplayStatus, string, error) {
	cur, err := pagination.Decode(rawCursor)
	if err != nil {
		return nil, nil, "", err
	}
	limit, rawCursor, err = pagination.Page(limit, rawCursor)
	if err != nil {
		return nil, nil, "", err
	}
	if f.AsOf.IsZero() {
		f.AsOf = s.now().UTC()
	}

	var items []Obligation
	var statuses []DisplayStatus
	err = database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewObligations); err != nil {
			return err
		}
		var err error
		items, statuses, err = s.repo.List(ctx, tx, orgID, f, limit, cur)
		return err
	})
	if err != nil {
		return nil, nil, "", err
	}

	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = pagination.Encode(last.CreatedAt, last.ID)
	}
	return items, statuses, next, nil
}

// Get returns a single obligation with derived status.
func (s *Service) Get(ctx context.Context, actor identity.Identity, orgID, obligationID uuid.UUID) (Obligation, DisplayStatus, error) {
	var ob Obligation
	var status DisplayStatus
	err := database.WithReadTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := orgaccess.Authorize(ctx, tx, orgID, actor, authz.CanViewObligations); err != nil {
			return err
		}
		var err error
		ob, err = s.repo.Get(ctx, tx, orgID, obligationID)
		return err
	})
	if err != nil {
		return Obligation{}, DisplayStatus{}, err
	}
	if ob.ID == uuid.Nil {
		return Obligation{}, DisplayStatus{}, apperr.ErrNotFound
	}

	// Resolve any submission state for derived status.
	subState, err := s.repo.LatestSubmissionState(ctx, s.pool, orgID, ob.ID)
	if err != nil {
		return ob, DisplayStatus{}, err
	}
	status = deriveStatus(subState, ob.DueAt, s.now())
	return ob, status, nil
}

// deriveStatus computes display status from a submission state and due date.
func deriveStatus(subState *ReviewState, dueAt time.Time, now time.Time) DisplayStatus {
	var state ReviewState
	if subState != nil {
		state = *subState
	} else {
		state = AwaitingSubmission
	}
	status, _ := Status(state, dueAt, now)
	return status
}

// Errors
var (
	ErrNoDueDay = errors.New("requirement version has no due day")
)

// CheckVersionEligibleForMonthly checks if a version can generate monthly obligations.
func CheckVersionEligibleForMonthly(rv interface {
	GetCadence() string
	GetDueDay() *int
}) error {
	if rv.GetCadence() != "monthly" {
		return fmt.Errorf("requirement version is not monthly cadence")
	}
	if rv.GetDueDay() == nil {
		return ErrNoDueDay
	}
	return nil
}
