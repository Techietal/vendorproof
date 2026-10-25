// Package contractors owns organization contractors and their lifecycle.
// Contractors are never hard-deleted; deactivation only removes eligibility for
// new engagements.
package contractors

import (
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/optional"
	"vendorproof/internal/validate"
)

// Contractor is an external service provider registered by one tenant.
type Contractor struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	Name           string
	ContactEmail   *string
	IsActive       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Filter narrows a tenant-scoped list.
type Filter struct {
	Active *bool
	Search string
}

// Patch is a partial update. ContactEmail null clears the address.
type Patch struct {
	Name         optional.Value[string]
	ContactEmail optional.Value[string]
}

// Normalize trims the list search term.
func (f *Filter) Normalize() error {
	search, err := validate.Search(f.Search)
	if err != nil {
		return err
	}
	f.Search = search
	return nil
}

// NormalizePatch validates a PATCH body. At least one field is required.
func (p Patch) NormalizePatch() (Patch, error) {
	if !p.Name.Set && !p.ContactEmail.Set {
		return Patch{}, apperr.Validation("body", "must contain at least one of name or contactEmail")
	}
	out := Patch{}
	if p.Name.Set {
		if p.Name.Value == nil {
			return Patch{}, apperr.Validation("name", "must not be null")
		}
		name, err := validate.Name(*p.Name.Value)
		if err != nil {
			return Patch{}, err
		}
		out.Name = optional.Value[string]{Set: true, Value: &name}
	}
	if p.ContactEmail.Set {
		if p.ContactEmail.Value == nil {
			out.ContactEmail = optional.Value[string]{Set: true, Value: nil}
		} else {
			email, err := validate.Email(*p.ContactEmail.Value)
			if err != nil {
				return Patch{}, err
			}
			if email == nil {
				// Supplying a blank string clears the address.
				out.ContactEmail = optional.Value[string]{Set: true, Value: nil}
			} else {
				out.ContactEmail = optional.Value[string]{Set: true, Value: email}
			}
		}
	}
	return out, nil
}
