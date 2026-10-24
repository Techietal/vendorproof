package organizations

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
)

const (
	MaxNameLen      = 200
	DefaultPageSize = 50
	MaxPageSize     = 200
	maxIssuerLen    = 2048
	maxSubjectLen   = 255
	maxCursorLen    = 512
)

var (
	// ErrNotFound is returned when an organization or membership is invisible
	// to the caller. Cross-tenant probes deliberately collapse into this error.
	ErrNotFound = errors.New("resource not found")
	// ErrForbidden means the caller is a member but lacks the required role.
	ErrForbidden = errors.New("insufficient permissions")
	// ErrConflict is a business or uniqueness conflict.
	ErrConflict = errors.New("conflict")
	// ErrLastOwner means the change would leave an organization ownerless.
	ErrLastOwner = errors.New("organization must retain an active owner")
	// ErrValidation carries field-level input errors.
	ErrValidation = errors.New("validation failed")
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

// ValidationError describes invalid request fields.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string        { return "validation failed" }
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

func validationErr(field, msg string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: msg}}
}

// NormalizeName trims and validates an organization name.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", validationErr("name", "must not be blank")
	}
	if utf8.RuneCountInString(name) > MaxNameLen {
		return "", validationErr("name", fmt.Sprintf("must be at most %d characters", MaxNameLen))
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", validationErr("name", "must not contain control characters")
		}
	}
	return name, nil
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
		return "", validationErr("role", "must be owner, manager, or reviewer")
	}
	return role, nil
}

// Page normalizes pagination inputs.
func Page(limit int, cursor string) (int, string, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return 0, "", validationErr("limit", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	}
	cursor = strings.TrimSpace(cursor)
	if len(cursor) > maxCursorLen {
		return 0, "", validationErr("cursor", "is too long")
	}
	return limit, cursor, nil
}
