package authz

import "testing"

func TestRoleParsing(t *testing.T) {
	for _, valid := range []string{"owner", "manager", "reviewer"} {
		if _, err := ParseRole(valid); err != nil {
			t.Errorf("%s rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Owner", "admin", " owner"} {
		if _, err := ParseRole(invalid); err == nil {
			t.Errorf("%q accepted", invalid)
		}
	}
}

func TestPermissions(t *testing.T) {
	cases := []struct {
		role          Role
		view, update  bool
		manageMembers bool
	}{
		{Owner, true, true, true},
		{Manager, true, false, false},
		{Reviewer, true, false, false},
		{Role(""), false, false, false},
	}
	for _, tc := range cases {
		if got := CanViewOrganization(tc.role); got != tc.view {
			t.Errorf("%s view=%v", tc.role, got)
		}
		if got := CanUpdateOrganization(tc.role); got != tc.update {
			t.Errorf("%s update=%v", tc.role, got)
		}
		if got := CanManageMemberships(tc.role); got != tc.manageMembers {
			t.Errorf("%s manage=%v", tc.role, got)
		}
	}
}

func TestManagerCannotDoOwnerOperations(t *testing.T) {
	if CanUpdateOrganization(Manager) {
		t.Error("manager can update organization")
	}
	if CanManageMemberships(Manager) {
		t.Error("manager can manage memberships")
	}
	if CanUpdateOrganization(Reviewer) {
		t.Error("reviewer can update organization")
	}
}
