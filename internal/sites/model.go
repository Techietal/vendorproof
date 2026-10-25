// Package sites owns organization work sites and their lifecycle. Sites are
// never hard-deleted; deactivation only removes eligibility for new
// engagements.
package sites

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/validate"
)

// Site is one organization-owned location.
type Site struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	Name           string
	IsActive       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Filter narrows a tenant-scoped list.
type Filter struct {
	Active *bool
	Search string
}

// NormalizeName validates a site name.
func NormalizeName(raw string) (string, error) {
	name, err := validate.Name(raw)
	if err != nil {
		return "", err
	}
	return name, nil
}

// Normalize validates the name search term.
func (f *Filter) Normalize() error {
	search, err := validate.Search(f.Search)
	if err != nil {
		return err
	}
	f.Search = search
	return nil
}

// ValidateNameField converts a missing/null PATCH name into a field error.
func ValidateNameField(raw *string) (string, error) {
	if raw == nil {
		return "", apperr.Validation("name", "must not be null")
	}
	return NormalizeName(*raw)
}
