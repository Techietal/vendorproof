package obligations

import (
	"time"

	"github.com/google/uuid"
)

// Obligation is a persisted monthly evidence obligation. It is immutable once
// created: the engagement, requirement family, version, period, coverage, and
// due date never change. Only the lifecycle state may advance via Phase 4
// submission/review workflows.
type Obligation struct {
	ID                   uuid.UUID
	OrganizationID       uuid.UUID
	EngagementID         uuid.UUID
	RequirementKey       uuid.UUID
	RequirementVersionID uuid.UUID
	PeriodStart          time.Time
	PeriodEnd            time.Time
	CoverageStart        time.Time
	CoverageEnd          time.Time
	DueAt                time.Time
	State                ReviewState
	CreatedAt            time.Time
}

// ListFilter narrows obligation listing queries.
type ListFilter struct {
	ContractorID         *uuid.UUID
	SiteID               *uuid.UUID
	EngagementID         *uuid.UUID
	RequirementKey       *uuid.UUID
	RequirementVersionID *uuid.UUID
	PeriodStart          *string // YYYY-MM
	DueAfter             *string
	DueBefore            *string
	State                *ReviewState
	OverdueOnly          bool
	AsOf                 time.Time
}

// DisplayStatus is the derived view returned to callers.
type DisplayStatus struct {
	ReviewState       ReviewState
	ContractorOverdue bool
}
