package sites_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
	"vendorproof/internal/sites"
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

func newService(pool *pgxpool.Pool) *sites.Service { return sites.NewService(pool) }

func TestCreateGetUpdateAndAudit(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)

	created, err := svc.Create(ctx, owner, orgID, "  Factory A ")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Factory A" || !created.IsActive || created.OrganizationID != orgID {
		t.Fatalf("created = %+v", created)
	}
	got, err := svc.Get(ctx, owner, orgID, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("get = %+v %v", got, err)
	}
	name := " Factory B "
	updated, err := svc.Update(ctx, owner, orgID, created.ID, &name)
	if err != nil || updated.Name != "Factory B" || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("updated = %+v %v", updated, err)
	}
	// Same name is a no-op without another audit row.
	if _, err := svc.Update(ctx, owner, orgID, created.ID, &name); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND resource_id=$2 AND action='site.created'`, orgID, created.ID); got != 1 {
		t.Fatalf("create audit = %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE organization_id=$1 AND resource_id=$2 AND action='site.updated'`, orgID, created.ID); got != 1 {
		t.Fatalf("update audit = %d", got)
	}
}

func TestCreateValidation(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	orgID, owner := seedOrg(t, pool)
	for _, name := range []string{"", "   ", "A\nB"} {
		if _, err := svc.Create(context.Background(), owner, orgID, name); !errors.Is(err, apperr.ErrValidation) {
			t.Errorf("%q: %v", name, err)
		}
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM sites`); got != 0 {
		t.Fatalf("sites = %d", got)
	}
}

func TestListTenantScopeFiltersAndPagination(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgA, ownerA := seedOrg(t, pool)
	orgB, ownerB := seedOrg(t, pool)

	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		if _, err := svc.Create(ctx, ownerA, orgA, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Create(ctx, ownerB, orgB, "Other tenant"); err != nil {
		t.Fatal(err)
	}
	// orgB's owner is not involved here; ownerA can still read orgB only if member.
	if _, _, err := svc.List(ctx, ownerA, orgB, sites.Filter{}, 50, ""); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-tenant list = %v", err)
	}
	items, _, err := svc.List(ctx, ownerA, orgA, sites.Filter{}, 50, "")
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d err %v", len(items), err)
	}
	search := "a"
	items, _, err = svc.List(ctx, ownerA, orgA, sites.Filter{Search: search}, 50, "")
	if err != nil || len(items) != 3 {
		t.Fatalf("search a = %d %v", len(items), err)
	}
	search = "Bravo"
	items, _, err = svc.List(ctx, ownerA, orgA, sites.Filter{Search: search}, 50, "")
	if err != nil || len(items) != 1 || items[0].Name != "Bravo" {
		t.Fatalf("search Bravo = %+v %v", items, err)
	}

	active := true
	first, next, err := svc.List(ctx, ownerA, orgA, sites.Filter{Active: &active}, 2, "")
	if err != nil || len(first) != 2 || next == "" {
		t.Fatalf("page 1 = %d next %q err %v", len(first), next, err)
	}
	second, next2, err := svc.List(ctx, ownerA, orgA, sites.Filter{Active: &active}, 2, next)
	if err != nil || len(second) != 1 || next2 != "" {
		t.Fatalf("page 2 = %d next %q err %v", len(second), next2, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range append(first, second...) {
		if seen[item.ID] {
			t.Fatalf("duplicate %s", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("unique = %d", len(seen))
	}
	if _, _, err := svc.List(ctx, ownerA, orgA, sites.Filter{}, 500, ""); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("limit = %v", err)
	}
	if _, _, err := svc.List(ctx, ownerA, orgA, sites.Filter{}, 50, "bogus"); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("cursor = %v", err)
	}
}

func TestAuthorization(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	site, err := svc.Create(ctx, owner, orgID, "Factory A")
	if err != nil {
		t.Fatal(err)
	}
	manager := addMember(t, pool, orgID, "manager", authz.Manager)
	reviewer := addMember(t, pool, orgID, "reviewer", authz.Reviewer)

	if _, err := svc.Create(ctx, manager, orgID, "Factory B"); err != nil {
		t.Fatalf("manager create: %v", err)
	}
	name := "Renamed"
	if _, err := svc.Update(ctx, manager, orgID, site.ID, &name); err != nil {
		t.Fatalf("manager update: %v", err)
	}
	if _, err := svc.Deactivate(ctx, manager, orgID, site.ID); err != nil && !errors.Is(err, apperr.ErrDeactivationBlocked) {
		t.Fatalf("manager deactivate: %v", err)
	}
	if _, err := svc.Get(ctx, reviewer, orgID, site.ID); err != nil {
		t.Fatalf("reviewer read: %v", err)
	}
	if _, _, err := svc.List(ctx, reviewer, orgID, sites.Filter{}, 50, ""); err != nil {
		t.Fatalf("reviewer list: %v", err)
	}
	if _, err := svc.Create(ctx, reviewer, orgID, "Factory C"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("reviewer create = %v", err)
	}
	if _, err := svc.Update(ctx, reviewer, orgID, site.ID, &name); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("reviewer update = %v", err)
	}
	outsider := identity.Identity{Issuer: testIssuer, Subject: "outsider"}
	if _, err := svc.Get(ctx, outsider, orgID, site.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("outsider = %v", err)
	}
	if _, err := svc.Create(ctx, outsider, orgID, "X"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("outsider create = %v", err)
	}
}

func TestCrossTenantAndMissingSites(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgA, ownerA := seedOrg(t, pool)
	orgB, ownerB := seedOrg(t, pool)
	foreign, err := svc.Create(ctx, ownerB, orgB, "B site")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, ownerA, orgA, foreign.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-tenant get = %v", err)
	}
	name := "X"
	if _, err := svc.Update(ctx, ownerA, orgA, foreign.ID, &name); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-tenant update = %v", err)
	}
	if _, err := svc.Deactivate(ctx, ownerA, orgA, foreign.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-tenant deactivate = %v", err)
	}
	if _, err := svc.Get(ctx, ownerA, orgA, uuid.New()); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func insertEngagement(t *testing.T, pool *pgxpool.Pool, orgID, siteID uuid.UUID, from time.Time, until *time.Time) uuid.UUID {
	t.Helper()
	contractorID := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name) VALUES ($1, $2, 'C')`, contractorID, orgID)
	id := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from, active_until)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, orgID, contractorID, siteID, from, until)
	return id
}

func today() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestDeactivationSemantics(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)

	blockedSite, err := svc.Create(ctx, owner, orgID, "Blocked")
	if err != nil {
		t.Fatal(err)
	}
	insertEngagement(t, pool, orgID, blockedSite.ID, today(), nil)
	if _, err := svc.Deactivate(ctx, owner, orgID, blockedSite.ID); !errors.Is(err, apperr.ErrDeactivationBlocked) {
		t.Fatalf("open engagement = %v", err)
	}

	futureSite, err := svc.Create(ctx, owner, orgID, "Future")
	if err != nil {
		t.Fatal(err)
	}
	until := today().AddDate(0, 1, 0)
	insertEngagement(t, pool, orgID, futureSite.ID, today().AddDate(0, 0, 5), &until)
	if _, err := svc.Deactivate(ctx, owner, orgID, futureSite.ID); !errors.Is(err, apperr.ErrDeactivationBlocked) {
		t.Fatalf("future engagement = %v", err)
	}

	pastSite, err := svc.Create(ctx, owner, orgID, "Past")
	if err != nil {
		t.Fatal(err)
	}
	yesterday := today().AddDate(0, 0, -1)
	ended := today().AddDate(0, 0, -1)
	insertEngagement(t, pool, orgID, pastSite.ID, yesterday.AddDate(0, 0, -10), &ended)
	deactivated, err := svc.Deactivate(ctx, owner, orgID, pastSite.ID)
	if err != nil || deactivated.IsActive {
		t.Fatalf("past = %+v %v", deactivated, err)
	}
	// Historical engagement is retained.
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements WHERE organization_id=$1 AND site_id=$2`, orgID, pastSite.ID); got != 1 {
		t.Fatalf("history = %d", got)
	}
	// Repeated deactivate is idempotent and writes no extra audit.
	if _, err := svc.Deactivate(ctx, owner, orgID, pastSite.ID); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='site.deactivated'`, pastSite.ID); got != 1 {
		t.Fatalf("deactivate audit = %d", got)
	}
	reactivated, err := svc.Reactivate(ctx, owner, orgID, pastSite.ID)
	if err != nil || !reactivated.IsActive {
		t.Fatalf("reactivate = %+v %v", reactivated, err)
	}
	if _, err := svc.Reactivate(ctx, owner, orgID, pastSite.ID); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='site.reactivated'`, pastSite.ID); got != 1 {
		t.Fatalf("reactivate audit = %d", got)
	}
	// Old engagement was not revived.
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements WHERE organization_id=$1 AND site_id=$2`, orgID, pastSite.ID); got != 1 {
		t.Fatalf("engagements after reactivation = %d", got)
	}
}

func TestRepeatedDeactivateOnNeverActiveSite(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	site, err := svc.Create(ctx, owner, orgID, "S")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Deactivate(ctx, owner, orgID, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Deactivate(ctx, owner, orgID, site.ID); err != nil {
		t.Fatalf("repeat = %v", err)
	}
}
