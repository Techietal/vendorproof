// Package authz contains organization-scoped role decisions. It is pure and is
// applied by application services after a membership has been loaded.
package authz

import "fmt"

// Role is an organization-scoped role.
type Role string

const (
	Owner    Role = "owner"
	Manager  Role = "manager"
	Reviewer Role = "reviewer"
)

// ParseRole validates an external role value.
func ParseRole(raw string) (Role, error) {
	switch Role(raw) {
	case Owner, Manager, Reviewer:
		return Role(raw), nil
	default:
		return "", fmt.Errorf("invalid role %q", raw)
	}
}

// Valid reports whether the role is known.
func (r Role) Valid() bool {
	switch r {
	case Owner, Manager, Reviewer:
		return true
	default:
		return false
	}
}

// CanViewOrganization allows every active member.
func CanViewOrganization(r Role) bool { return r.Valid() }

// CanUpdateOrganization is owner-only in Phase 1.
func CanUpdateOrganization(r Role) bool { return r == Owner }

// CanManageMemberships is owner-only. Managers gain operational permissions in
// later phases but cannot administer membership.
func CanManageMemberships(r Role) bool { return r == Owner }
