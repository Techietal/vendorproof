// Package apperr holds transport-neutral business error classes shared by
// application services and translated to HTTP by handlers.
package apperr

import "errors"

var (
	// ErrNotFound collapses missing and cross-tenant resources.
	ErrNotFound = errors.New("resource not found")
	// ErrForbidden means an active member lacks the required role.
	ErrForbidden = errors.New("insufficient permissions")
	// ErrConflict is a business conflict.
	ErrConflict = errors.New("conflict")
	// ErrDeactivationBlocked means active or future engagements remain.
	ErrDeactivationBlocked = errors.New("active or future engagements remain")
	// ErrInactiveContractor / ErrInactiveSite reject new engagements.
	ErrInactiveContractor = errors.New("contractor is inactive")
	ErrInactiveSite       = errors.New("site is inactive")
	// ErrImmutable means downstream history depends on the current dates.
	ErrImmutable = errors.New("historical references prevent this change")
	// ErrValidation is the sentinel matched by ValidationError.
	ErrValidation = errors.New("validation failed")
)

// ValidationError carries field-level input errors.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string        { return ErrValidation.Error() }
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Validation builds a single-field validation error.
func Validation(field, msg string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: msg}}
}
