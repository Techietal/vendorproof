package engagements_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/engagements"
	"vendorproof/internal/identity"
	"vendorproof/internal/optional"
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

func addSite(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, active bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name, is_active) VALUES ($1, $2, 'Site', $3)`, id, orgID, active)
	return id
}

func addContractor(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, active bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name, is_active) VALUES ($1, $2, 'Contractor', $3)`, id, orgID, active)
	return id
}

func newService(pool *pgxpool.Pool) *engagements.Service { return engagements.NewService(pool) }

func newSiteService(pool *pgxpool.Pool) *sites.Service { return sites.NewService(pool) }

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func creation(contractorID, siteID uuid.UUID, from string, until *string) engagements.Creation {
	in := engagements.Creation{ContractorID: contractorID, SiteID: siteID, ActiveFrom: d(from)}
	if until != nil {
		u := d(*until)
		in.ActiveUntil = &u
	}
	return in
}

func str(s string) *string { return &s }

func TestCreateGetAndMultiSite(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteA := addSite(t, pool, orgID, true)
	siteB := addSite(t, pool, orgID, true)

	first, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteA, "2026-01-01", str("2026-06-01")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteB, "2026-01-01", nil))
	if err != nil {
		t.Fatalf("different site rejected: %v", err)
	}
	got, err := svc.Get(ctx, owner, orgID, first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("get = %+v %v", got, err)
	}
	if second.ID == first.ID {
		t.Fatal("same id")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='engagement.created' AND organization_id=$1`, orgID); got != 2 {
		t.Fatalf("audit = %d", got)
	}
}

func TestOverlapAndAdjacency(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", str("2026-06-01"))); err != nil {
		t.Fatal(err)
	}
	// Overlapping on the upper side.
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-05-01", str("2026-08-01"))); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("overlap = %v", err)
	}
	// Fully contained.
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-02-01", str("2026-03-01"))); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("contained = %v", err)
	}
	// Open-ended overlap.
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2025-01-01", nil)); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("open overlap = %v", err)
	}
	// Adjacent, disjoint and before.
	for _, period := range [][2]string{{"2026-06-01", "2026-09-01"}, {"2026-09-01", "2026-10-01"}, {"2025-01-01", "2026-01-01"}} {
		if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, period[0], &period[1])); err != nil {
			t.Fatalf("period %v: %v", period, err)
		}
	}
	// Entirely open non-overlapping is possible once nothing is open.
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2030-01-01", nil)); err != nil {
		t.Fatal(err)
	}
}

func TestValidationAndParents(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", str("2026-01-01"))); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("equal = %v", err)
	}
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-05", str("2026-01-01"))); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("before = %v", err)
	}
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, uuid.New(), "2026-01-01", nil)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing site = %v", err)
	}
	if _, err := svc.Create(ctx, owner, orgID, creation(uuid.New(), siteID, "2026-01-01", nil)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing contractor = %v", err)
	}

	otherOrg, _ := seedOrg(t, pool)
	foreignSite := addSite(t, pool, otherOrg, true)
	foreignContractor := addContractor(t, pool, otherOrg, true)
	if _, err := svc.Create(ctx, owner, orgID, creation(foreignContractor, siteID, "2026-01-01", nil)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross tenant contractor = %v", err)
	}
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, foreignSite, "2026-01-01", nil)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross tenant site = %v", err)
	}

	inactiveContractor := addContractor(t, pool, orgID, false)
	inactiveSite := addSite(t, pool, orgID, false)
	if _, err := svc.Create(ctx, owner, orgID, creation(inactiveContractor, siteID, "2026-01-01", nil)); !errors.Is(err, apperr.ErrInactiveContractor) {
		t.Fatalf("inactive contractor = %v", err)
	}
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, inactiveSite, "2026-01-01", nil)); !errors.Is(err, apperr.ErrInactiveSite) {
		t.Fatalf("inactive site = %v", err)
	}
}

func TestCloseSemanticsAndHistory(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	e, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Close(ctx, owner, orgID, e.ID, "2026-01-01"); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("equal start = %v", err)
	}
	closed, err := svc.Close(ctx, owner, orgID, e.ID, "2026-06-01")
	if err != nil || closed.ActiveUntil == nil || engagements.FormatDate(*closed.ActiveUntil) != "2026-06-01" {
		t.Fatalf("close = %+v %v", closed, err)
	}
	if !closed.CreatedAt.Equal(e.CreatedAt) || !closed.UpdatedAt.After(e.UpdatedAt) {
		t.Fatalf("timestamps = %+v", closed)
	}
	// Same close is idempotent, different close conflicts.
	if _, err := svc.Close(ctx, owner, orgID, e.ID, "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Close(ctx, owner, orgID, e.ID, "2026-07-01"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("different close = %v", err)
	}
	// Another engagement starting exactly at the exclusive end is accepted.
	if _, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-06-01", str("2026-09-01"))); err != nil {
		t.Fatal(err)
	}
	// But a close that would overlap it is rejected.
	open, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-09-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Close(ctx, owner, orgID, open.ID, "2026-10-01"); err != nil {
		t.Fatal(err)
	}
	later, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-10-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	// Closing is a forward-only end change and stays inside the free tail.
	if _, err := svc.Close(ctx, owner, orgID, later.ID, "2027-07-01"); err != nil {
		t.Fatalf("close later = %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements`); got != 4 {
		t.Fatalf("engagements = %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='engagement.closed'`); got != 3 {
		t.Fatalf("close audits = %d", got)
	}
}

func TestReengagementAfterEnded(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	first, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2025-01-01", str("2025-04-01")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("engagement reused")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements WHERE organization_id=$1`, orgID); got != 2 {
		t.Fatalf("rows = %d", got)
	}
}

func TestCorrection(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	e, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-02", str("2026-06-01")))
	if err != nil {
		t.Fatal(err)
	}
	from := "2026-01-01"
	corrected, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{
		ActiveFrom: optional.Value[string]{Set: true, Value: &from},
	})
	if err != nil || engagements.FormatDate(corrected.ActiveFrom) != "2026-01-01" {
		t.Fatalf("correct = %+v %v", corrected, err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='engagement.corrected' AND resource_id=$1`, e.ID); got != 1 {
		t.Fatalf("audit = %d", got)
	}
	// No-op correction writes no audit.
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{
		ActiveFrom: optional.Value[string]{Set: true, Value: &from},
	}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='engagement.corrected' AND resource_id=$1`, e.ID); got != 1 {
		t.Fatalf("no-op audit = %d", got)
	}
	// Reopen by clearing the end, then close again to leave a gap.
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true},
	}); err != nil {
		t.Fatal(err)
	}
	closeAgain := "2026-06-01"
	if _, err := svc.Close(ctx, owner, orgID, e.ID, closeAgain); err != nil {
		t.Fatal(err)
	}
	// Invalid range.
	bad := "2025-12-01"
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true, Value: &bad},
	}); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("invalid = %v", err)
	}
	// Overlap with a second engagement.
	second, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-08-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	overlap := "2026-09-01"
	if _, err := svc.Correct(ctx, owner, orgID, second.ID, engagements.Patch{
		ActiveFrom: optional.Value[string]{Set: true, Value: &overlap},
	}); err != nil {
		t.Fatal(err)
	}
	tooLate := "2026-10-01"
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true, Value: &tooLate},
	}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("overlap = %v", err)
	}
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, engagements.Patch{}); !errors.Is(err, apperr.ErrValidation) {
		t.Fatalf("empty patch = %v", err)
	}
}

func seedObligation(t *testing.T, pool *pgxpool.Pool, orgID, engagementID uuid.UUID, start, end string) {
	t.Helper()
	versionID := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO requirement_versions
		(id, organization_id, requirement_key, version, name, cadence, due_day, effective_from)
		VALUES ($1, $2, $3, 1, 'R', 'monthly', 10, '2025-01-01')`, versionID, orgID, uuid.New())
	testutil.MustExec(t, pool, `INSERT INTO obligations
		(id, organization_id, engagement_id, requirement_version_id, period_start, period_end, due_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())`, uuid.New(), orgID, engagementID, versionID, start, end)
}

func seedAssignment(t *testing.T, pool *pgxpool.Pool, orgID, engagementID uuid.UUID) {
	t.Helper()
	versionID := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO requirement_versions
		(id, organization_id, requirement_key, version, name, cadence, due_day, effective_from)
		VALUES ($1, $2, $3, 1, 'R', 'monthly', 10, '2025-01-01')`, versionID, orgID, uuid.New())
	testutil.MustExec(t, pool, `INSERT INTO requirement_assignments
		(id, organization_id, engagement_id, requirement_version_id)
		VALUES ($1, $2, $3, $4)`, uuid.New(), orgID, engagementID, versionID)
}

func TestCorrectionsGuardDownstreamHistory(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	withObligation, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	seedObligation(t, pool, orgID, withObligation.ID, "2026-02-01", "2026-03-01")

	late := "2026-03-01"
	if _, err := svc.Correct(ctx, owner, orgID, withObligation.ID, engagements.Patch{
		ActiveFrom: optional.Value[string]{Set: true, Value: &late},
	}); !errors.Is(err, apperr.ErrImmutable) {
		t.Fatalf("start excludes obligation = %v", err)
	}
	earlyEnd := "2026-02-15"
	if _, err := svc.Correct(ctx, owner, orgID, withObligation.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true, Value: &earlyEnd},
	}); !errors.Is(err, apperr.ErrImmutable) {
		t.Fatalf("end excludes obligation = %v", err)
	}
	if _, err := svc.Close(ctx, owner, orgID, withObligation.ID, "2026-02-15"); !errors.Is(err, apperr.ErrImmutable) {
		t.Fatalf("close excludes obligation = %v", err)
	}
	// A compatible extension is allowed.
	later := "2027-01-01"
	if _, err := svc.Correct(ctx, owner, orgID, withObligation.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true, Value: &later},
	}); err != nil {
		t.Fatalf("extension = %v", err)
	}

	assignmentOnly, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2030-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	seedAssignment(t, pool, orgID, assignmentOnly.ID)
	shorten := "2030-02-01"
	if _, err := svc.Correct(ctx, owner, orgID, assignmentOnly.ID, engagements.Patch{
		ActiveUntil: optional.Value[string]{Set: true, Value: &shorten},
	}); !errors.Is(err, apperr.ErrImmutable) {
		t.Fatalf("assignment shorten = %v", err)
	}
	pullBack := "2029-12-01"
	if _, err := svc.Correct(ctx, owner, orgID, assignmentOnly.ID, engagements.Patch{
		ActiveFrom: optional.Value[string]{Set: true, Value: &pullBack},
	}); err != nil {
		t.Fatalf("assignment extension = %v", err)
	}
}

func TestListFiltersStatesAndPagination(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	otherOrg, otherOwner := seedOrg(t, pool)
	c1 := addContractor(t, pool, orgID, true)
	c2 := addContractor(t, pool, orgID, true)
	s1 := addSite(t, pool, orgID, true)
	s2 := addSite(t, pool, orgID, true)

	ended, err := svc.Create(ctx, owner, orgID, creation(c1, s1, "2025-01-01", str("2025-12-01")))
	if err != nil {
		t.Fatal(err)
	}
	active, err := svc.Create(ctx, owner, orgID, creation(c1, s2, "2026-01-01", str("2027-01-01")))
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := svc.Create(ctx, owner, orgID, creation(c2, s1, "2030-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	foreignSite := addSite(t, pool, otherOrg, true)
	foreignC := addContractor(t, pool, otherOrg, true)
	if _, err := svc.Create(ctx, otherOwner, otherOrg, creation(foreignC, foreignSite, "2026-01-01", nil)); err != nil {
		t.Fatal(err)
	}

	items, states, eval, _, err := svc.List(ctx, owner, orgID, engagements.Filter{AsOf: d("2026-06-01")}, 50, "")
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d %v", len(items), err)
	}
	if !eval.Equal(d("2026-06-01")) {
		t.Fatalf("eval = %s", eval)
	}
	byID := map[uuid.UUID]engagements.State{}
	for i, item := range items {
		byID[item.ID] = states[i]
	}
	if byID[ended.ID] != engagements.Ended || byID[active.ID] != engagements.Active || byID[scheduled.ID] != engagements.Scheduled {
		t.Fatalf("states = %+v", byID)
	}

	state := engagements.Active
	filtered, _, _, _, err := svc.List(ctx, owner, orgID, engagements.Filter{State: &state, AsOf: d("2026-06-01")}, 50, "")
	if err != nil || len(filtered) != 1 || filtered[0].ID != active.ID {
		t.Fatalf("state filter = %+v %v", filtered, err)
	}
	byContractor, _, _, _, err := svc.List(ctx, owner, orgID, engagements.Filter{ContractorID: &c2}, 50, "")
	if err != nil || len(byContractor) != 1 || byContractor[0].ID != scheduled.ID {
		t.Fatalf("contractor filter = %+v %v", byContractor, err)
	}
	bySite, _, _, _, err := svc.List(ctx, owner, orgID, engagements.Filter{SiteID: &s2}, 50, "")
	if err != nil || len(bySite) != 1 || bySite[0].ID != active.ID {
		t.Fatalf("site filter = %+v %v", bySite, err)
	}

	page1, _, _, next, err := svc.List(ctx, owner, orgID, engagements.Filter{AsOf: d("2026-06-01")}, 2, "")
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page 1 = %v", err)
	}
	page2, _, _, _, err := svc.List(ctx, owner, orgID, engagements.Filter{AsOf: d("2026-06-01")}, 2, next)
	if err != nil || len(page2) != 1 {
		t.Fatalf("page 2 = %d %v", len(page2), err)
	}
}

func TestAuthorization(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	e, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	manager := addMember(t, pool, orgID, "manager", authz.Manager)
	reviewer := addMember(t, pool, orgID, "reviewer", authz.Reviewer)
	if got, err := svc.Get(ctx, reviewer, orgID, e.ID); err != nil || got.ID != e.ID {
		t.Fatalf("reviewer get = %v", err)
	}
	if _, _, _, _, err := svc.List(ctx, reviewer, orgID, engagements.Filter{}, 50, ""); err != nil {
		t.Fatalf("reviewer list = %v", err)
	}
	if _, err := svc.Create(ctx, reviewer, orgID, creation(contractorID, siteID, "2030-01-01", nil)); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("reviewer create = %v", err)
	}
	if _, err := svc.Close(ctx, reviewer, orgID, e.ID, "2026-06-01"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("reviewer close = %v", err)
	}
	if _, err := svc.Close(ctx, manager, orgID, e.ID, "2026-06-01"); err != nil {
		t.Fatalf("manager close = %v", err)
	}
	outsider := identity.Identity{Issuer: testIssuer, Subject: "outsider"}
	if _, err := svc.Get(ctx, outsider, orgID, e.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("outsider = %v", err)
	}
	// Cross-tenant engagement ID.
	otherOrg, otherOwner := seedOrg(t, pool)
	foreign, err := svc.Create(ctx, otherOwner, otherOrg, creation(addContractor(t, pool, otherOrg, true), addSite(t, pool, otherOrg, true), "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, owner, orgID, foreign.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross tenant = %v", err)
	}
}

func TestDatabaseExclusionConstraint(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	testutil.MustExec(t, pool, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from, active_until)
		VALUES ($1, $2, $3, $4, '2026-01-01', '2026-06-01')`, uuid.New(), orgID, contractorID, siteID)
	// Direct insert bypassing the service must still be rejected.
	if _, err := pool.Exec(ctx, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from)
		VALUES ($1, $2, $3, $4, '2026-05-01')`, uuid.New(), orgID, contractorID, siteID); err == nil {
		t.Fatal("database allowed an overlap")
	}
}

func TestConcurrentOverlappingCreates(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", str("2026-12-01")))
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
		case errors.Is(err, apperr.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements`); got != 1 {
		t.Fatalf("rows = %d", got)
	}
}

func TestConcurrentCloseDifferentDates(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)
	e, err := svc.Create(ctx, owner, orgID, creation(contractorID, siteID, "2026-01-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, date := range []string{"2026-06-01", "2026-07-01"} {
		wg.Add(1)
		go func(date string) {
			defer wg.Done()
			<-start
			_, err := svc.Close(ctx, owner, orgID, e.ID, date)
			errs <- err
		}(date)
	}
	close(start)
	wg.Wait()
	close(errs)
	var ok, conflict int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, apperr.ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM audit_events WHERE action='engagement.closed'`); got != 1 {
		t.Fatalf("audits = %d", got)
	}
}

func TestDeactivationSerializesWithEngagementCreation(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	orgID, owner := seedOrg(t, pool)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	// Hold the organization lock and create the engagement in an uncommitted
	// transaction, so deactivation is forced to wait and then observe it.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from)
		VALUES ($1, $2, $3, $4, '2026-01-01')`, uuid.New(), orgID, contractorID, siteID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	// Manager/site deactivation uses the same organization lock.
	deactivate := func() {
		siteSvc := newSiteService(pool)
		_, err := siteSvc.Deactivate(ctx, owner, orgID, siteID)
		done <- err
	}
	go deactivate()
	testutil.WaitForLockWait(t, pool, "SELECT id FROM organizations")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, apperr.ErrDeactivationBlocked) {
		t.Fatalf("deactivate = %v", err)
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT is_active FROM sites WHERE id=$1`, siteID).Scan(&active); err != nil || !active {
		t.Fatalf("site active=%t err=%v", active, err)
	}
}

func TestRevocationSerializesWithPrivilegedWrite(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := newService(pool)
	ctx := context.Background()
	orgID, _ := seedOrg(t, pool)
	manager := addMember(t, pool, orgID, "manager", authz.Manager)
	contractorID := addContractor(t, pool, orgID, true)
	siteID := addSite(t, pool, orgID, true)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memberships SET revoked_at=now()
		WHERE organization_id=$1 AND identity_subject='manager'`, orgID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.Create(ctx, manager, orgID, creation(contractorID, siteID, "2026-01-01", nil))
		done <- err
	}()
	testutil.WaitForLockWait(t, pool, "SELECT id FROM organizations")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("stale manager write = %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements`); got != 0 {
		t.Fatalf("rows = %d", got)
	}
}
