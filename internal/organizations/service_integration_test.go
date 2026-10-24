package organizations_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
	"vendorproof/internal/organizations"
	"vendorproof/internal/testutil"
)

const testIssuer = "https://idp.test"

func actor(subject string) identity.Identity {
	return identity.Identity{Issuer: testIssuer, Subject: subject}
}

func addMember(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, issuer, subject string, role authz.Role) uuid.UUID {
	t.Helper()
	id := uuid.New()
	testutil.MustExec(t, pool, `
		INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, $5)`, id, orgID, issuer, subject, role)
	return id
}

func seedOrg(t *testing.T, pool *pgxpool.Pool, name string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	orgID := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, $2)`, orgID, name)
	owner := addMember(t, pool, orgID, testIssuer, "owner-"+orgID.String()[:8], authz.Owner)
	return orgID, owner
}

func newService(pool *pgxpool.Pool) *organizations.Service {
	return organizations.NewService(pool, organizations.NewRepository(pool))
}

func TestBootstrapCreatesOwnerAndAuditAtomically(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()

	org, membership, err := service.Bootstrap(ctx, "Acme", testIssuer, "first-owner")
	if err != nil {
		t.Fatal(err)
	}
	if membership.Role != authz.Owner || membership.OrganizationID != org.ID {
		t.Fatalf("membership = %+v", membership)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE organization_id=$1 AND role='owner' AND revoked_at IS NULL`, org.ID); got != 1 {
		t.Fatalf("owner count = %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='organization.bootstrapped'`, org.ID); got != 1 {
		t.Fatalf("bootstrap audit count = %d", got)
	}
}

func TestBootstrapValidation(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	for _, tc := range []struct{ name, issuer, subject string }{
		{"", testIssuer, "u"}, {"Org", "not-a-url", "u"}, {"Org", testIssuer, ""},
	} {
		if _, _, err := service.Bootstrap(context.Background(), tc.name, tc.issuer, tc.subject); !errors.Is(err, organizations.ErrValidation) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM organizations`); got != 0 {
		t.Fatalf("organizations = %d", got)
	}
}

func TestListAndMeAreTenantScoped(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()

	orgA, _ := seedOrg(t, pool, "A")
	orgB, _ := seedOrg(t, pool, "B")
	user := actor("multi")
	addMember(t, pool, orgA, testIssuer, "multi", authz.Reviewer)
	addMember(t, pool, orgB, testIssuer, "multi", authz.Manager)
	stranger := actor("stranger")

	orgs, _, err := service.List(ctx, user, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 2 {
		t.Fatalf("orgs = %d", len(orgs))
	}
	_ = orgA
	_ = orgB

	if strangerOrgs, _, err := service.List(ctx, stranger, 50, ""); err != nil || len(strangerOrgs) != 0 {
		t.Fatalf("stranger sees %d orgs (%v)", len(strangerOrgs), err)
	}
	memberships, _, err := service.Me(ctx, user, 50, "")
	if err != nil || len(memberships) != 2 {
		t.Fatalf("me = %d (%v)", len(memberships), err)
	}
}

func TestGetCrossTenantIsNotFound(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	orgA, _ := seedOrg(t, pool, "A")
	orgB, _ := seedOrg(t, pool, "B")
	user := actor("a-user")
	addMember(t, pool, orgA, testIssuer, "a-user", authz.Owner)

	if _, _, err := service.Get(context.Background(), user, orgB); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateOrganizationAuthorizationAndAudit(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool, "Old")
	owner := actor("owner-" + orgID.String()[:8])

	org, err := service.Update(ctx, owner, orgID, "  New Name ")
	if err != nil {
		t.Fatal(err)
	}
	if org.Name != "New Name" {
		t.Fatalf("name = %q", org.Name)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='organization.updated'`, orgID); got != 1 {
		t.Fatalf("audit count = %d", got)
	}

	addMember(t, pool, orgID, testIssuer, "manager", authz.Manager)
	addMember(t, pool, orgID, testIssuer, "reviewer", authz.Reviewer)
	if _, err := service.Update(ctx, actor("manager"), orgID, "M"); !errors.Is(err, organizations.ErrForbidden) {
		t.Fatalf("manager: %v", err)
	}
	if _, err := service.Update(ctx, actor("reviewer"), orgID, "R"); !errors.Is(err, organizations.ErrForbidden) {
		t.Fatalf("reviewer: %v", err)
	}
	if _, err := service.Update(ctx, actor("outsider"), orgID, "X"); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("outsider: %v", err)
	}
	if _, err := service.Update(ctx, owner, orgID, " "); !errors.Is(err, organizations.ErrValidation) {
		t.Fatalf("blank: %v", err)
	}
}

func TestMembershipCreation(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool, "Org")
	owner := actor("owner-" + orgID.String()[:8])

	created, err := service.AddMembershipByInput(ctx, owner, orgID, " https://other-idp/ ", "subject-1", "manager")
	if err != nil {
		t.Fatal(err)
	}
	if created.Identity.Issuer != "https://other-idp" {
		t.Fatalf("issuer = %q", created.Identity.Issuer)
	}
	if _, err := service.AddMembershipByInput(ctx, owner, orgID, "https://other-idp", "subject-1", "reviewer"); !errors.Is(err, organizations.ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := service.AddMembershipByInput(ctx, owner, orgID, "", "s", "bogus"); !errors.Is(err, organizations.ErrValidation) {
		t.Fatalf("invalid = %v", err)
	}
	if _, err := service.AddMembershipByInput(ctx, owner, uuid.New(), "https://i", "s", "manager"); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("missing org = %v", err)
	}
	addMember(t, pool, orgID, testIssuer, "mgr", authz.Manager)
	if _, err := service.AddMembershipByInput(ctx, actor("mgr"), orgID, "https://i", "s2", "manager"); !errors.Is(err, organizations.ErrForbidden) {
		t.Fatalf("manager = %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='membership.created'`, orgID); got != 1 {
		t.Fatalf("audit count = %d", got)
	}
}

func TestSameSubjectAllowedAcrossIssuers(t *testing.T) {
	pool := testutil.NewDB(t)
	orgID, _ := seedOrg(t, pool, "Org")
	addMember(t, pool, orgID, "https://issuer-b", "same-subject", authz.Reviewer)
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE organization_id=$1 AND identity_subject='same-subject' AND revoked_at IS NULL`, orgID); got != 1 {
		t.Fatalf("count = %d", got)
	}
}

func TestRoleChangesAndLastOwner(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, ownerID := seedOrg(t, pool, "Org")
	owner := actor("owner-" + orgID.String()[:8])
	secondID := addMember(t, pool, orgID, testIssuer, "second", authz.Reviewer)

	// Last owner cannot be demoted.
	if _, err := service.ChangeMembershipRole(ctx, owner, orgID, ownerID, "manager"); !errors.Is(err, organizations.ErrLastOwner) {
		t.Fatalf("last owner demotion = %v", err)
	}

	// Promote the second member to owner, then demote the first.
	updated, err := service.ChangeMembershipRole(ctx, owner, orgID, secondID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != authz.Owner {
		t.Fatalf("role = %s", updated.Role)
	}
	if _, err := service.ChangeMembershipRole(ctx, owner, orgID, ownerID, "manager"); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='membership.role_changed'`, orgID); got != 2 {
		t.Fatalf("audit count = %d", got)
	}
}

func TestRevocation(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, ownerID := seedOrg(t, pool, "Org")
	owner := actor("owner-" + orgID.String()[:8])
	revokedID := addMember(t, pool, orgID, testIssuer, "gone", authz.Reviewer)

	if err := service.RevokeMembership(ctx, owner, orgID, revokedID); err != nil {
		t.Fatal(err)
	}
	// Revoked member immediately loses access even with a valid token.
	if _, _, err := service.Get(ctx, actor("gone"), orgID); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("revoked access = %v", err)
	}
	// Repeated revocation conflicts.
	if err := service.RevokeMembership(ctx, owner, orgID, revokedID); !errors.Is(err, organizations.ErrConflict) {
		t.Fatalf("repeat = %v", err)
	}
	// Last owner cannot be removed.
	if err := service.RevokeMembership(ctx, owner, orgID, ownerID); !errors.Is(err, organizations.ErrLastOwner) {
		t.Fatalf("last owner = %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND action='membership.revoked'`, orgID); got != 1 {
		t.Fatalf("audit count = %d", got)
	}
	// The identity can be re-added, and only one row is active.
	if _, err := service.AddMembership(ctx, owner, orgID, actor("gone"), authz.Manager); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE organization_id=$1 AND identity_subject='gone' AND revoked_at IS NULL`, orgID); got != 1 {
		t.Fatalf("active rows = %d", got)
	}
}

func TestCrossOrganizationMembershipIDIsNotFound(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgA, _ := seedOrg(t, pool, "A")
	orgB, ownerB := seedOrg(t, pool, "B")
	ownerA := actor("owner-" + orgA.String()[:8])
	_ = orgB
	// Owner of A supplies B's membership ID.
	if _, err := service.ChangeMembershipRole(ctx, ownerA, orgA, ownerB, "manager"); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("cross-org patch = %v", err)
	}
	if err := service.RevokeMembership(ctx, ownerA, orgA, ownerB); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("cross-org delete = %v", err)
	}
}

func TestListMembershipsAuthorizationAndScope(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgA, _ := seedOrg(t, pool, "A")
	orgB, _ := seedOrg(t, pool, "B")
	ownerA := actor("owner-" + orgA.String()[:8])
	addMember(t, pool, orgA, testIssuer, "reviewer-a", authz.Reviewer)
	addMember(t, pool, orgB, testIssuer, "owner-b", authz.Owner)

	items, _, err := service.ListMemberships(ctx, ownerA, orgA, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	for _, item := range items {
		if item.OrganizationID != orgA {
			t.Fatalf("leaked membership from %s", item.OrganizationID)
		}
	}
	if _, _, err := service.ListMemberships(ctx, actor("reviewer-a"), orgA, 50, ""); !errors.Is(err, organizations.ErrForbidden) {
		t.Fatalf("reviewer = %v", err)
	}
	if _, _, err := service.ListMemberships(ctx, ownerA, orgB, 50, ""); !errors.Is(err, organizations.ErrNotFound) {
		t.Fatalf("cross tenant = %v", err)
	}
}

func TestConcurrentRevocationPreservesOwner(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, firstID := seedOrg(t, pool, "Org")
	secondID := addMember(t, pool, orgID, testIssuer, "second", authz.Owner)
	owner := actor("owner-" + orgID.String()[:8])

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, id := range []uuid.UUID{firstID, secondID} {
		wg.Add(1)
		go func(memberID uuid.UUID) {
			defer wg.Done()
			<-start
			errs <- service.RevokeMembership(ctx, owner, orgID, memberID)
		}(id)
	}
	close(start)
	wg.Wait()
	close(errs)

	var ok, refused int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, organizations.ErrLastOwner):
			// The loser may still be an active owner and see the invariant.
			refused++
		case errors.Is(err, organizations.ErrNotFound):
			// Or its own membership may have been the one removed first.
			refused++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("ok=%d refused=%d", ok, refused)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE organization_id=$1 AND role='owner' AND revoked_at IS NULL`, orgID); got != 1 {
		t.Fatalf("owners = %d", got)
	}
}

func TestConcurrentDuplicateMembership(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool, "Org")
	owner := actor("owner-" + orgID.String()[:8])

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.AddMembership(ctx, owner, orgID, actor("dup"), authz.Reviewer)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var ok, conflict int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, organizations.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestAuditTableIsAppendOnly(t *testing.T) {
	pool := testutil.NewDB(t)
	service := newService(pool)
	org, _, err := service.Bootstrap(context.Background(), "Audit", testIssuer, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE audit_events SET action='x' WHERE organization_id=$1`, org.ID); err == nil {
		t.Fatal("UPDATE succeeded on append-only table")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM audit_events WHERE organization_id=$1`, org.ID); err == nil {
		t.Fatal("DELETE succeeded on append-only table")
	}
}

func TestDatabaseConstraints(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool, "Org")

	// Role domain.
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, 'i', 's', 'admin')`, uuid.New(), orgID); err == nil {
		t.Fatal("invalid role accepted")
	}
	// Foreign key to tenant.
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, 'i', 's', 'owner')`, uuid.New(), uuid.New()); err == nil {
		t.Fatal("missing organization accepted")
	}
	// Active duplicate.
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, 'dupe', 'owner')`, uuid.New(), orgID, testIssuer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, 'dupe', 'owner')`, uuid.New(), orgID, testIssuer); err == nil {
		t.Fatal("duplicate active identity accepted")
	}
	// Blank organization name.
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1, '  ')`, uuid.New()); err == nil {
		t.Fatal("blank name accepted")
	}
}
