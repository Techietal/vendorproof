package contractors_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/contractors"
	"vendorproof/internal/identity"
	"vendorproof/internal/optional"
	"vendorproof/internal/testutil"
)

const testIssuer = "https://idp.test"

func seedOrg(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, identity.Identity) {
	t.Helper()
	orgID := uuid.New()
	owner := identity.Identity{Issuer: testIssuer, Subject: "owner-" + orgID.String()[:8]}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, 'owner')`, uuid.New(), orgID, owner.Issuer, owner.Subject)
	return orgID, owner
}

func addMember(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, subject string, role authz.Role) identity.Identity {
	t.Helper()
	ident := identity.Identity{Issuer: testIssuer, Subject: subject}
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, $5)`, uuid.New(), orgID, ident.Issuer, ident.Subject, role)
	return ident
}

func newService(pool *pgxpool.Pool) *contractors.Service { return contractors.NewService(pool) }

func TestCreateWithOptionalEmailAndAudit(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)

	withEmail, err := svc.Create(ctx, owner, orgID, " ABC Security ", " Contact@Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if withEmail.ContactEmail == nil || *withEmail.ContactEmail != "Contact@example.com" {
		t.Fatalf("email = %v", withEmail.ContactEmail)
	}
	without, err := svc.Create(ctx, owner, orgID, "CleanPro", "")
	if err != nil || without.ContactEmail != nil {
		t.Fatalf("no email = %+v %v", without, err)
	}
	got, err := svc.Get(ctx, owner, orgID, withEmail.ID)
	if err != nil || got.ID != withEmail.ID {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='contractor.created' AND resource_id=$1`, withEmail.ID); got != 1 {
		t.Fatalf("audit = %d", got)
	}
	if _, err := svc.Create(ctx, owner, orgID, "Bad", "nope"); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("invalid email = %v", err)
	}
}

func TestUpdatePreservesOmittedAndClearsNull(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	c, err := svc.Create(ctx, owner, orgID, "CleanPro", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	name := " CleanPro Housekeeping "
	updated, err := svc.Update(ctx, owner, orgID, c.ID, contractors.Patch{
		Name: optional.Value[string]{Set: true, Value: &name},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "CleanPro Housekeeping" || updated.ContactEmail == nil || *updated.ContactEmail != "a@example.com" {
		t.Fatalf("omitted email lost: %+v", updated)
	}
	cleared, err := svc.Update(ctx, owner, orgID, c.ID, contractors.Patch{
		ContactEmail: optional.Value[string]{Set: true},
	})
	if err != nil || cleared.ContactEmail != nil {
		t.Fatalf("clear = %+v %v", cleared, err)
	}
	// No-op patch writes no audit.
	before := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='contractor.updated' AND resource_id=$1`, c.ID)
	if _, err := svc.Update(ctx, owner, orgID, c.ID, contractors.Patch{
		ContactEmail: optional.Value[string]{Set: true},
	}); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='contractor.updated' AND resource_id=$1`, c.ID); after != before {
		t.Fatalf("audit changed %d -> %d", before, after)
	}
}

func TestAuthorizationAndTenantScope(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgA, _ := seedOrg(t, pool)
	orgB, ownerB := seedOrg(t, pool)
	foreign, err := svc.Create(ctx, ownerB, orgB, "Foreign", "f@example.com")
	if err != nil {
		t.Fatal(err)
	}
	reviewer := addMember(t, pool, orgA, "reviewer", authz.Reviewer)
	manager := addMember(t, pool, orgA, "manager", authz.Manager)

	if _, err := svc.Get(ctx, reviewer, orgA, foreign.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross tenant = %v", err)
	}
	if _, err := svc.Create(ctx, reviewer, orgA, "R", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("reviewer = %v", err)
	}
	created, err := svc.Create(ctx, manager, orgA, "Managed", "")
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if _, _, err := svc.List(ctx, reviewer, orgA, contractors.Filter{}, 50, ""); err != nil {
		t.Fatalf("reviewer list: %v", err)
	}
	if _, err := svc.Get(ctx, reviewer, orgA, created.ID); err != nil {
		t.Fatalf("reviewer get: %v", err)
	}
}

func TestListFiltersAndIsolation(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	otherOrg, otherOwner := seedOrg(t, pool)
	for _, name := range []string{"Alpha", "Bravo"} {
		if _, err := svc.Create(ctx, owner, orgID, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(ctx, otherOwner, otherOrg, "Other", ""); err != nil {
		t.Fatal(err)
	}
	items, _, err := svc.List(ctx, owner, orgID, contractors.Filter{}, 50, "")
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %d %v", len(items), err)
	}
	search := "Brav"
	items, _, err = svc.List(ctx, owner, orgID, contractors.Filter{Search: search}, 50, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("search = %d %v", len(items), err)
	}
	active := true
	items, _, err = svc.List(ctx, owner, orgID, contractors.Filter{Active: &active}, 50, "")
	if err != nil || len(items) != 2 {
		t.Fatalf("active = %d %v", len(items), err)
	}
}

func TestDeactivationBlocksAndPreservesHistory(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	blocked, err := svc.Create(ctx, owner, orgID, "Blocked", "")
	if err != nil {
		t.Fatal(err)
	}
	siteID, engagementID := uuid.New(), uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name) VALUES ($1, $2, 'S')`, siteID, orgID)
	testutil.MustExec(t, pool, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from)
		VALUES ($1, $2, $3, $4, current_date)`, engagementID, orgID, blocked.ID, siteID)
	if _, err := svc.Deactivate(ctx, owner, orgID, blocked.ID); !errors.Is(err, apperr.ErrDeactivationBlocked) {
		t.Fatalf("blocked = %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements WHERE id=$1`, engagementID); got != 1 {
		t.Fatal("history lost")
	}

	ended, err := svc.Create(ctx, owner, orgID, "Ended", "")
	if err != nil {
		t.Fatal(err)
	}
	testutil.MustExec(t, pool, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from, active_until)
		VALUES ($1, $2, $3, $4, current_date - 10, current_date - 1)`, uuid.New(), orgID, ended.ID, siteID)
	deactivated, err := svc.Deactivate(ctx, owner, orgID, ended.ID)
	if err != nil || deactivated.IsActive {
		t.Fatalf("ended = %+v %v", deactivated, err)
	}
	if _, err := svc.Deactivate(ctx, owner, orgID, ended.ID); err != nil {
		t.Fatalf("idempotent = %v", err)
	}
	reactivated, err := svc.Reactivate(ctx, owner, orgID, ended.ID)
	if err != nil || !reactivated.IsActive {
		t.Fatalf("reactivate = %+v %v", reactivated, err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='contractor.deactivated'`, ended.ID); got != 1 {
		t.Fatalf("audit = %d", got)
	}
}
