package organizations

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
	"vendorproof/internal/pagination"
	"vendorproof/internal/validate"
)

const (
	MaxNameLen      = validate.MaxNameLen
	DefaultPageSize = pagination.DefaultPageSize
	MaxPageSize     = pagination.MaxPageSize
	maxIssuerLen    = 2048
	maxSubjectLen   = 255
)

var (
	// ErrNotFound is returned when an organization or membership is invisible
	// to the caller. Cross-tenant probes deliberately collapse into this error.
	ErrNotFound = apperr.ErrNotFound
	// ErrForbidden means the caller is a member but lacks the required role.
	ErrForbidden = apperr.ErrForbidden
	// ErrConflict is a business or uniqueness conflict.
	ErrConflict = apperr.ErrConflict
	// ErrLastOwner means the change would leave an organization ownerless.
	ErrLastOwner = errors.New("organization must retain an active owner")
	// ErrValidation carries field-level input errors.
	ErrValidation = apperr.ErrValidation
)

// Organization is one tenant.
type Organization struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OrganizationWithRole is an organization together with the caller's role.
// CursorAt/CursorID identify the membership row that keyset pagination is
// anchored to; they differ from the organization's own timestamps.
type OrganizationWithRole struct {
	Organization
	Role     authz.Role
	CursorAt time.Time
	CursorID uuid.UUID
}

// Membership binds an issuer-qualified identity to an organization.
type Membership struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	Identity       identity.Identity
	Role           authz.Role
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RevokedAt      *time.Time
}

// MembershipWithOrganization is used by GET /v1/me.
type MembershipWithOrganization struct {
	Membership
	OrganizationName string
}

// ValidationError describes invalid request fields. It is shared with every
// Phase 2 module.
type ValidationError = apperr.ValidationError

// NormalizeName trims and validates an organization name.
func NormalizeName(raw string) (string, error) {
	return validate.Name(raw)
}

// NormalizeIdentity validates an admin-supplied issuer/subject pair.
func NormalizeIdentity(issuer, subject string) (identity.Identity, error) {
	issuer = identity.NormalizeIssuer(issuer)
	subject = strings.TrimSpace(subject)
	fields := map[string]string{}
	if issuer == "" {
		fields["issuer"] = "must not be blank"
	} else if len(issuer) > maxIssuerLen {
		fields["issuer"] = "is too long"
	} else if !hasHTTPScheme(issuer) {
		fields["issuer"] = "must be an absolute http(s) URL"
	}
	if subject == "" {
		fields["subject"] = "must not be blank"
	} else if len(subject) > maxSubjectLen || !identity.ValidateSubject(subject) {
		fields["subject"] = "is invalid"
	}
	if len(fields) > 0 {
		return identity.Identity{}, &ValidationError{Fields: fields}
	}
	return identity.Identity{Issuer: issuer, Subject: subject}, nil
}

func hasHTTPScheme(raw string) bool {
	return strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")
}

// ParseRole validates a role value with a field-level error.
func ParseRole(raw string) (authz.Role, error) {
	role, err := authz.ParseRole(raw)
	if err != nil {
		return "", apperr.Validation("role", "must be owner, manager, or reviewer")
	}
	return role, nil
}

// Page normalizes pagination inputs.
func Page(limit int, cursor string) (int, string, error) {
	return pagination.Page(limit, cursor)
}

func validationErr(field, msg string) *ValidationError {
	return apperr.Validation(field, msg)
}
