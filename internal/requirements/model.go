// Package requirements owns stable requirement families and their immutable
// published versions. A requirement family is a logical concept (for example,
// "Monthly Attendance Register"); each published version captures an immutable
// snapshot of scheduling rules effective from a given date.
package requirements

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/validate"
)

// Cadence controls how obligations are generated.
type Cadence string

const (
	CadenceMonthly Cadence = "monthly"
	CadenceExpiry  Cadence = "expiry"
)

// ParseCadence validates a cadence value.
func ParseCadence(raw string) (Cadence, error) {
	switch Cadence(raw) {
	case CadenceMonthly, CadenceExpiry:
		return Cadence(raw), nil
	default:
		return "", apperr.Validation("cadence", "must be monthly or expiry")
	}
}

// Requirement is a stable logical concept. Multiple immutable versions belong
// to the same family.
type Requirement struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	CreatedAt      time.Time
}

// RequirementVersion is an immutable snapshot of scheduling rules.
// DueDay is 1-31 of the FOLLOWING calendar month, clamped to the last day.
// EffectiveUntil is exclusive; nil is open-ended.
// ReportingTimezone is an IANA timezone identifier.
type RequirementVersion struct {
	ID                uuid.UUID
	OrganizationID    uuid.UUID
	RequirementKey    uuid.UUID
	Version           int
	Name              string
	Cadence           Cadence
	DueDay            int
	EffectiveFrom     time.Time
	EffectiveUntil    *time.Time
	ReportingTimezone string
	CreatedAt         time.Time
}

// FamilyVersion is a requirement family with its latest version number.
type FamilyVersion struct {
	Requirement
	LatestVersion int
}

// PublishInput validates a new version publication request.
type PublishInput struct {
	Name              string  `json:"name"`
	Cadence           string  `json:"cadence"`
	DueDay            *int    `json:"dueDay"`
	EffectiveFrom     string  `json:"effectiveFrom"`
	EffectiveUntil    *string `json:"effectiveUntil"`
	ReportingTimezone string  `json:"timezone"`
}

// Normalize validates and normalizes publish input.
func (p PublishInput) Normalize() (NormalizedPublish, error) {
	name, err := validate.Name(p.Name)
	if err != nil {
		return NormalizedPublish{}, err
	}
	cadence, err := ParseCadence(p.Cadence)
	if err != nil {
		return NormalizedPublish{}, err
	}
	loc, err := validate.LoadLocation(p.ReportingTimezone)
	if err != nil {
		return NormalizedPublish{}, err
	}
	from, err := validate.Date("effectiveFrom", p.EffectiveFrom)
	if err != nil {
		return NormalizedPublish{}, err
	}
	var until *time.Time
	if p.EffectiveUntil != nil {
		parsed, err := validate.Date("effectiveUntil", *p.EffectiveUntil)
		if err != nil {
			return NormalizedPublish{}, err
		}
		if !parsed.After(from) {
			return NormalizedPublish{}, apperr.Validation("effectiveUntil", "must be later than effectiveFrom")
		}
		until = &parsed
	}

	np := NormalizedPublish{
		Name:              name,
		Cadence:           cadence,
		ReportingTimezone: p.ReportingTimezone,
		EffectiveFrom:     from,
		EffectiveUntil:    until,
		Loc:               loc,
	}

	if cadence == CadenceMonthly {
		if p.DueDay == nil {
			return NormalizedPublish{}, apperr.Validation("dueDay", "is required for monthly cadence")
		}
		day := *p.DueDay
		if day < 1 || day > 31 {
			return NormalizedPublish{}, apperr.Validation("dueDay", "must be between 1 and 31")
		}
		np.DueDay = day
	} else {
		// Expiry cadence does not use due_day for monthly generation.
		np.DueDay = 0
	}

	return np, nil
}

// NormalizedPublish is validated publish input ready for persistence.
type NormalizedPublish struct {
	Name              string
	Cadence           Cadence
	DueDay            int
	EffectiveFrom     time.Time
	EffectiveUntil    *time.Time
	ReportingTimezone string
	Loc               *time.Location
}

// CreateInput validates a new requirement family creation request.
type CreateInput struct {
	Name              string  `json:"name"`
	Cadence           string  `json:"cadence"`
	DueDay            *int    `json:"dueDay"`
	EffectiveFrom     string  `json:"effectiveFrom"`
	EffectiveUntil    *string `json:"effectiveUntil"`
	ReportingTimezone string  `json:"timezone"`
}
