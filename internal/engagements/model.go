// Package engagements owns dated contractor-site assignments. Engagements are
// historical records: they are corrected or closed, never hard-deleted.
package engagements

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/optional"
	"vendorproof/internal/validate"
)

// State is derived from dates and an evaluation date; it is never stored.
type State string

const (
	Scheduled State = "scheduled"
	Active    State = "active"
	Ended     State = "ended"
)

// Engagement is one dated contractor-site period. ActiveUntil nil is
// open-ended; the interval is half-open [ActiveFrom, ActiveUntil).
type Engagement struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	ContractorID   uuid.UUID
	SiteID         uuid.UUID
	ActiveFrom     time.Time
	ActiveUntil    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Creation is validated create input.
type Creation struct {
	ContractorID uuid.UUID
	SiteID       uuid.UUID
	ActiveFrom   time.Time
	ActiveUntil  *time.Time
}

// Patch is a date-only correction. ActiveUntil null opens the period.
type Patch struct {
	ActiveFrom  optional.Value[string] `json:"activeFrom"`
	ActiveUntil optional.Value[string] `json:"activeUntil"`
}

// Filter narrows engagement lists.
type Filter struct {
	ContractorID *uuid.UUID
	SiteID       *uuid.UUID
	State        *State
	AsOf         time.Time
}

// StateAt derives the state for an evaluation date.
func StateAt(eval, from time.Time, until *time.Time) State {
	switch {
	case eval.Before(from):
		return Scheduled
	case until != nil && !eval.Before(*until):
		return Ended
	default:
		return Active
	}
}

// ValidatePeriod enforces the half-open interval invariant.
func ValidatePeriod(from time.Time, until *time.Time) error {
	if until != nil && !until.After(from) {
		return apperr.Validation("activeUntil", "must be later than activeFrom")
	}
	return nil
}

// ParseState validates a state filter.
func ParseState(raw string) (*State, error) {
	if raw == "" {
		return nil, nil
	}
	state := State(raw)
	switch state {
	case Scheduled, Active, Ended:
		return &state, nil
	default:
		return nil, apperr.Validation("state", "must be scheduled, active, or ended")
	}
}

// NormalizePatch validates supplied dates and requires at least one field.
func (p Patch) NormalizePatch() (Patch, error) {
	if !p.ActiveFrom.Set && !p.ActiveUntil.Set {
		return Patch{}, apperr.Validation("body", "must contain at least one of activeFrom or activeUntil")
	}
	out := Patch{}
	if p.ActiveFrom.Set {
		if p.ActiveFrom.Value == nil {
			return Patch{}, apperr.Validation("activeFrom", "must not be null")
		}
		from, err := validate.Date("activeFrom", *p.ActiveFrom.Value)
		if err != nil {
			return Patch{}, err
		}
		out.ActiveFrom = optional.Value[string]{Set: true, Value: formatDate(from)}
	}
	if p.ActiveUntil.Set {
		if p.ActiveUntil.Value == nil {
			out.ActiveUntil = optional.Value[string]{Set: true, Value: nil}
		} else {
			until, err := validate.Date("activeUntil", *p.ActiveUntil.Value)
			if err != nil {
				return Patch{}, err
			}
			out.ActiveUntil = optional.Value[string]{Set: true, Value: formatDate(until)}
		}
	}
	return out, nil
}

func formatDate(t time.Time) *string {
	s := t.Format(validate.DateLayout)
	return &s
}

// FormatDate renders the API date form.
func FormatDate(t time.Time) string { return t.Format(validate.DateLayout) }
