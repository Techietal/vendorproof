package obligations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/audit"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
)

// Generator persists monthly obligations to PostgreSQL.
// It uses the existing pure MonthlyRule calculation but adds
// assignment-effective periods, coverage intersection, and idempotent
// database insertion.
type Generator struct {
	pool   *pgxpool.Pool
	now    func() time.Time
	auditf func(ctx context.Context, q database.Querier, e audit.Event) error
}

func NewGenerator(pool *pgxpool.Pool, auditf func(ctx context.Context, q database.Querier, e audit.Event) error) *Generator {
	return &Generator{
		pool:   pool,
		now:    time.Now,
		auditf: auditf,
	}
}

// GenerationResult reports what happened during a generation run.
type GenerationResult struct {
	PeriodStart time.Time
	PeriodEnd   time.Time
	Created     int
	Existing    int
	Conflicts   int
	Skipped     int
}

// AssignmentView is a flattened view of an assignment with its version rules.
type AssignmentView struct {
	AssignmentID          uuid.UUID
	EngagementID          uuid.UUID
	RequirementKey        uuid.UUID
	RequirementVersionID  uuid.UUID
	EffectiveFrom         time.Time
	EffectiveUntil        *time.Time
	DueDay                int
	ReportingTimezone     string
	VersionEffectiveFrom  time.Time
	VersionEffectiveUntil *time.Time
}

// GenerateForMonth generates all monthly obligations for a given organization
// and reporting month. It is idempotent: re-running for the same org/month
// does not create duplicates. The db argument allows running within a transaction.
func (g *Generator) GenerateForMonth(ctx context.Context, db database.Querier, orgID uuid.UUID, year int, month int, dryRun bool) (GenerationResult, error) {
	periodStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	views, err := g.loadEligibleAssignments(ctx, db, orgID, periodStart, periodEnd)
	if err != nil {
		return GenerationResult{}, err
	}

	result := GenerationResult{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
	}

	if dryRun {
		for _, v := range views {
			coverageStart, _ := intersectCoverage(periodStart, periodEnd, v.EffectiveFrom, v.EffectiveUntil)
			if coverageStart.IsZero() {
				result.Skipped++
				continue
			}
			result.Created++
		}
		return result, nil
	}

	const batchSize = 50
	for i := 0; i < len(views); i += batchSize {
		end := i + batchSize
		if end > len(views) {
			end = len(views)
		}
		counts, err := g.generateBatch(ctx, db, orgID, periodStart, periodEnd, views[i:end])
		if err != nil {
			return result, err
		}
		result.Created += counts.created
		result.Existing += counts.existing
		result.Conflicts += counts.conflicts
		result.Skipped += counts.skipped
	}

	if g.auditf != nil {
		_ = g.auditf(ctx, db, audit.Event{
			OrganizationID: orgID,
			Actor:          identity.Identity{Issuer: "system", Subject: "cli"},
			Action:         audit.ActionObligationGenerated,
			ResourceType:   audit.ResourceObligation,
			ResourceID:     uuid.Nil,
			Details: map[string]any{
				"period_start": periodStart.Format("2006-01-02"),
				"period_end":   periodEnd.Format("2006-01-02"),
				"created":      result.Created,
				"existing":     result.Existing,
				"conflicts":    result.Conflicts,
				"skipped":      result.Skipped,
			},
		})
	}

	return result, nil
}

type batchCounts struct {
	created, existing, conflicts, skipped int
}

// generateBatch inserts obligations for a batch of assignments.
func (g *Generator) generateBatch(ctx context.Context, db database.Querier, orgID uuid.UUID, periodStart, periodEnd time.Time, views []AssignmentView) (batchCounts, error) {
	var counts batchCounts
	for _, v := range views {
		engagementFrom, engagementUntil := g.loadEngagementInterval(ctx, orgID, v.EngagementID)
		if engagementFrom.IsZero() {
			counts.skipped++
			continue
		}

		effectiveFrom := maxDate(periodStart, v.EffectiveFrom, engagementFrom, v.VersionEffectiveFrom)
		effectiveUntil := minDatePtr(v.EffectiveUntil, engagementUntil, v.VersionEffectiveUntil)

		coverageStart, coverageEnd := intersectCoverage(periodStart, periodEnd, effectiveFrom, effectiveUntil)
		if coverageStart.IsZero() {
			counts.skipped++
			continue
		}

		loc, err := loadLocationSafe(v.ReportingTimezone)
		if err != nil {
			counts.skipped++
			continue
		}

		rule := MonthlyRule{
			OrganizationID:       orgID.String(),
			EngagementID:         v.EngagementID.String(),
			RequirementVersionID: v.RequirementVersionID.String(),
			EffectiveFrom:        v.EffectiveFrom,
			EffectiveUntil:       v.EffectiveUntil,
			DueDay:               v.DueDay,
		}

		obligation, err := GenerateMonthly(rule, periodStart.Year(), time.Month(periodStart.Month()), loc)
		if err != nil {
			if err == ErrInactiveRule {
				counts.skipped++
				continue
			}
			return counts, err
		}

		inserted, err := g.insertObligation(ctx, db, orgID, v, obligation, coverageStart, coverageEnd, periodStart, periodEnd)
		if err != nil {
			if database.IsUniqueViolation(err, "obligations_family_month_key") {
				counts.existing++
				continue
			}
			if database.IsUniqueViolation(err, "obligations_engagement_version_period_key") {
				counts.existing++
				continue
			}
			return counts, err
		}
		if inserted {
			counts.created++
		} else {
			counts.existing++
		}
	}
	return counts, nil
}

// insertObligation attempts to insert an obligation. Returns true if inserted,
// false if it already existed.
func (g *Generator) insertObligation(ctx context.Context, db database.Querier, orgID uuid.UUID, v AssignmentView, obligation MonthlyObligation, coverageStart, coverageEnd, periodStart, periodEnd time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `
		INSERT INTO obligations (
			id, organization_id, engagement_id, requirement_version_id, requirement_key,
			period_start, period_end, coverage_start, coverage_end, due_at, state, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'awaiting_submission', now())
		ON CONFLICT (organization_id, engagement_id, requirement_key, period_start) DO NOTHING`,
		uuid.New(), orgID, v.EngagementID, v.RequirementVersionID, v.RequirementKey,
		periodStart, periodEnd, coverageStart, coverageEnd, obligation.DueAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func loadLocationSafe(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(tz)
}

// intersectCoverage returns the intersection of [periodStart, periodEnd)
// and [effectiveFrom, effectiveUntil). Returns zero values if empty.
func intersectCoverage(periodStart, periodEnd, effectiveFrom time.Time, effectiveUntil *time.Time) (time.Time, time.Time) {
	start := periodStart
	if effectiveFrom.After(start) {
		start = effectiveFrom
	}
	end := periodEnd
	if effectiveUntil != nil && effectiveUntil.Before(end) {
		end = *effectiveUntil
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}
	}
	return start, end
}

func maxDate(times ...time.Time) time.Time {
	var max time.Time
	for _, t := range times {
		if t.After(max) {
			max = t
		}
	}
	return max
}

func minDatePtr(times ...*time.Time) *time.Time {
	var min *time.Time
	for _, t := range times {
		if t == nil {
			continue
		}
		if min == nil || t.Before(*min) {
			min = t
		}
	}
	return min
}

// loadEligibleAssignments returns assignments that may generate obligations
// for the given reporting month.
func (g *Generator) loadEligibleAssignments(ctx context.Context, db database.Querier, orgID uuid.UUID, periodStart, periodEnd time.Time) ([]AssignmentView, error) {
	rows, err := db.Query(ctx, `
		SELECT ra.id, ra.engagement_id, ra.requirement_key, ra.requirement_version_id,
		       ra.effective_from, ra.effective_until,
		       rv.due_day, rv.reporting_timezone, rv.effective_from, rv.effective_until
		FROM requirement_assignments ra
		JOIN requirement_versions rv ON rv.id = ra.requirement_version_id
		WHERE ra.organization_id = $1
		  AND ra.effective_from <= $2::date
		  AND (ra.effective_until IS NULL OR ra.effective_until > $3::date)
		ORDER BY ra.id`,
		orgID, periodEnd, periodStart)
	if err != nil {
		return nil, fmt.Errorf("load eligible assignments: %w", err)
	}
	defer rows.Close()

	var views []AssignmentView
	for rows.Next() {
		var v AssignmentView
		var dueDay *int
		if err := rows.Scan(&v.AssignmentID, &v.EngagementID, &v.RequirementKey,
			&v.RequirementVersionID, &v.EffectiveFrom, &v.EffectiveUntil,
			&dueDay, &v.ReportingTimezone, &v.VersionEffectiveFrom, &v.VersionEffectiveUntil); err != nil {
			return nil, fmt.Errorf("scan assignment: %w", err)
		}
		if dueDay != nil {
			v.DueDay = *dueDay
		}
		views = append(views, v)
	}
	return views, rows.Err()
}

// loadEngagementInterval returns the engagement's active period.
func (g *Generator) loadEngagementInterval(ctx context.Context, orgID, engagementID uuid.UUID) (time.Time, *time.Time) {
	var from time.Time
	var until *time.Time
	err := g.pool.QueryRow(ctx, `
		SELECT active_from, active_until FROM engagements WHERE organization_id = $1 AND id = $2`,
		orgID, engagementID).Scan(&from, &until)
	if err != nil {
		return time.Time{}, nil
	}
	return from, until
}
