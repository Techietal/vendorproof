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

// Phase 2 operational permissions. Reviewers keep read-only access to the
// business directories; owners and managers may mutate them. Membership
// administration remains owner-only (CanManageMemberships).
func CanViewSites(r Role) bool { return r.Valid() }

func CanManageSites(r Role) bool { return r == Owner || r == Manager }

func CanViewContractors(r Role) bool { return r.Valid() }

func CanManageContractors(r Role) bool { return r == Owner || r == Manager }

func CanViewEngagements(r Role) bool { return r.Valid() }

func CanManageEngagements(r Role) bool { return r == Owner || r == Manager }

// Phase 3 operational permissions. Reviewers keep read-only access to
// requirements, assignments, and obligations. Owners and managers may
// create requirements, publish versions, and manage assignments. Generation
// is restricted to owners and managers.
func CanViewRequirements(r Role) bool { return r.Valid() }

func CanManageRequirements(r Role) bool { return r == Owner || r == Manager }

func CanViewAssignments(r Role) bool { return r.Valid() }

func CanManageAssignments(r Role) bool { return r == Owner || r == Manager }

func CanViewObligations(r Role) bool { return r.Valid() }

func CanGenerateObligations(r Role) bool { return r == Owner || r == Manager }
