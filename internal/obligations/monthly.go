package obligations

import (
	"errors"
	"fmt"
	"time"
)

// MonthlyRule defines a versioned requirement for a contractor engagement.
// EffectiveUntil is exclusive. Calendar dates are interpreted in loc.
type MonthlyRule struct {
	OrganizationID       string
	EngagementID         string
	RequirementVersionID string
	EffectiveFrom        time.Time
	EffectiveUntil       *time.Time
	DueDay               int // 1-31 of the FOLLOWING calendar month, clamped to the last day
}

type MonthlyObligation struct {
	OrganizationID       string
	EngagementID         string
	RequirementVersionID string
	PeriodStart          time.Time // inclusive
	PeriodEnd            time.Time // exclusive
	DueAt                time.Time
	IdempotencyKey       string
}

var ErrInactiveRule = errors.New("rule is not active during this reporting period")

// GenerateMonthly creates a deterministic obligation for the named reporting month.
// The caller must enforce uniqueness for (organization, engagement, rule version,
// period start) in persistent storage; the returned key is not a substitute for
// a database uniqueness constraint.
func GenerateMonthly(rule MonthlyRule, year int, month time.Month, loc *time.Location) (MonthlyObligation, error) {
	if loc == nil || year < 2000 || year > 2200 || month < time.January || month > time.December {
		return MonthlyObligation{}, errors.New("invalid reporting period or timezone")
	}
	if rule.OrganizationID == "" || rule.EngagementID == "" || rule.RequirementVersionID == "" || rule.EffectiveFrom.IsZero() || rule.DueDay < 1 || rule.DueDay > 31 {
		return MonthlyObligation{}, errors.New("invalid monthly rule")
	}
	start := time.Date(year, month, 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	// Evaluate rule validity by the calendar date of the rule boundaries.
	effectiveFrom := midnight(rule.EffectiveFrom.In(loc), loc)
	if !effectiveFrom.Before(end) {
		return MonthlyObligation{}, ErrInactiveRule
	}
	if rule.EffectiveUntil != nil {
		effectiveUntil := midnight(rule.EffectiveUntil.In(loc), loc)
		if !effectiveUntil.After(effectiveFrom) {
			return MonthlyObligation{}, errors.New("invalid rule effective interval")
		}
		if !effectiveUntil.After(start) {
			return MonthlyObligation{}, ErrInactiveRule
		}
	}

	dueMonth := end
	lastDay := time.Date(dueMonth.Year(), dueMonth.Month()+1, 0, 0, 0, 0, 0, loc).Day()
	day := rule.DueDay
	if day > lastDay {
		day = lastDay
	}
	due := time.Date(dueMonth.Year(), dueMonth.Month(), day, 17, 0, 0, 0, loc)
	key := fmt.Sprintf("%s/%s/%s/%04d-%02d", rule.OrganizationID, rule.EngagementID, rule.RequirementVersionID, year, int(month))
	return MonthlyObligation{
		OrganizationID:       rule.OrganizationID,
		EngagementID:         rule.EngagementID,
		RequirementVersionID: rule.RequirementVersionID,
		PeriodStart:          start,
		PeriodEnd:            end,
		DueAt:                due.UTC(),
		IdempotencyKey:       key,
	}, nil
}

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

type ReviewState string

const (
	AwaitingSubmission ReviewState = "awaiting_submission"
	AwaitingReview     ReviewState = "awaiting_review"
	NeedsCorrection    ReviewState = "needs_correction"
	Accepted           ReviewState = "accepted"
)

type DisplayStatus struct {
	ReviewState       ReviewState
	ContractorOverdue bool
}

// Status derives lateness rather than persisting a potentially stale overdue flag.
// Awaiting internal review is never treated as a contractor delay.
func Status(state ReviewState, dueAt, now time.Time) (DisplayStatus, error) {
	switch state {
	case AwaitingSubmission, AwaitingReview, NeedsCorrection, Accepted:
	default:
		return DisplayStatus{}, errors.New("unknown review state")
	}
	if dueAt.IsZero() {
		return DisplayStatus{}, errors.New("missing due date")
	}
	overdue := now.After(dueAt) && (state == AwaitingSubmission || state == NeedsCorrection)
	return DisplayStatus{ReviewState: state, ContractorOverdue: overdue}, nil
}
