// Package assignments connects published requirement versions to contractor-site
// engagements with explicit effective date ranges. Assignments are dated so
// that version transitions can be prospective without rewriting history.
package assignments

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/validate"
)

// Assignment connects a requirement version to an engagement for a date range.
// The interval [EffectiveFrom, EffectiveUntil) is half-open; nil EffectiveUntil
// is open-ended.
type Assignment struct {
	ID                   uuid.UUID
	OrganizationID       uuid.UUID
	EngagementID         uuid.UUID
	RequirementKey       uuid.UUID
	RequirementVersionID uuid.UUID
	EffectiveFrom        time.Time
	EffectiveUntil       *time.Time
	AssignedAt           time.Time
}

// CreateInput validates a new assignment request.
type CreateInput struct {
	EngagementID         string  `json:"engagementId"`
	RequirementVersionID string  `json:"requirementVersionId"`
	EffectiveFrom        string  `json:"effectiveFrom"`
	EffectiveUntil       *string `json:"effectiveUntil"`
}

// NormalizedCreate is validated create input.
type NormalizedCreate struct {
	EngagementID         uuid.UUID
	RequirementVersionID uuid.UUID
	EffectiveFrom        time.Time
	EffectiveUntil       *time.Time
}

// Normalize validates and converts input.
func (in CreateInput) Normalize() (NormalizedCreate, error) {
	engagementID, err := uuid.Parse(in.EngagementID)
	if err != nil {
		return NormalizedCreate{}, apperr.Validation("engagementId", "must be a UUID")
	}
	versionID, err := uuid.Parse(in.RequirementVersionID)
	if err != nil {
		return NormalizedCreate{}, apperr.Validation("requirementVersionId", "must be a UUID")
	}
	from, err := validate.Date("effectiveFrom", in.EffectiveFrom)
	if err != nil {
		return NormalizedCreate{}, err
	}
	var until *time.Time
	if in.EffectiveUntil != nil {
		parsed, err := validate.Date("effectiveUntil", *in.EffectiveUntil)
		if err != nil {
			return NormalizedCreate{}, err
		}
		if !parsed.After(from) {
			return NormalizedCreate{}, apperr.Validation("effectiveUntil", "must be later than effectiveFrom")
		}
		until = &parsed
	}
	return NormalizedCreate{
		EngagementID:         engagementID,
		RequirementVersionID: versionID,
		EffectiveFrom:        from,
		EffectiveUntil:       until,
	}, nil
}

// Filter narrows assignment lists.
type Filter struct {
	EngagementID   *uuid.UUID
	RequirementKey *uuid.UUID
}

// TransitionInput specifies a new version and effective date for a transition.
type TransitionInput struct {
	RequirementVersionID string `json:"requirementVersionId"`
	EffectiveFrom        string `json:"effectiveFrom"`
}

// NormalizedTransition is validated transition input.
type NormalizedTransition struct {
	RequirementVersionID uuid.UUID
	EffectiveFrom        time.Time
}

// Normalize validates transition input.
func (in TransitionInput) Normalize() (NormalizedTransition, error) {
	versionID, err := uuid.Parse(in.RequirementVersionID)
	if err != nil {
		return NormalizedTransition{}, apperr.Validation("requirementVersionId", "must be a UUID")
	}
	from, err := validate.Date("effectiveFrom", in.EffectiveFrom)
	if err != nil {
		return NormalizedTransition{}, err
	}
	return NormalizedTransition{
		RequirementVersionID: versionID,
		EffectiveFrom:        from,
	}, nil
}
