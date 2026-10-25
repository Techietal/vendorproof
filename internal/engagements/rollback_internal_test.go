package engagements

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/audit"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/optional"
	"vendorproof/internal/testutil"
)

func rollbackSeed(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, identity.Identity, uuid.UUID, uuid.UUID) {
	t.Helper()
	orgID := uuid.New()
	owner := identity.Identity{Issuer: "https://idp", Subject: "owner"}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, 'https://idp', 'owner', 'owner')`, uuid.New(), orgID)
	siteID, contractorID := uuid.New(), uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name) VALUES ($1, $2, 'S')`, siteID, orgID)
	testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name) VALUES ($1, $2, 'C')`, contractorID, orgID)
	return orgID, owner, siteID, contractorID
}

func failAudit(context.Context, database.Querier, audit.Event) error {
	return errors.New("audit unavailable")
}

func TestMutationsRollBackWhenAuditFails(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := NewService(pool)
	orgID, owner, siteID, contractorID := rollbackSeed(t, pool)
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	e, err := svc.Create(ctx, owner, orgID, Creation{ContractorID: contractorID, SiteID: siteID, ActiveFrom: from})
	if err != nil {
		t.Fatal(err)
	}
	pastUntil := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	otherSite, otherContractor := uuid.New(), uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name) VALUES ($1, $2, 'S2')`, otherSite, orgID)
	testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name) VALUES ($1, $2, 'C2')`, otherContractor, orgID)
	closed, err := svc.Create(ctx, owner, orgID, Creation{
		ContractorID: otherContractor, SiteID: otherSite, ActiveFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), ActiveUntil: &pastUntil,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.auditf = failAudit

	second := from.AddDate(1, 0, 0)
	if _, err := svc.Create(ctx, owner, orgID, Creation{ContractorID: contractorID, SiteID: siteID, ActiveFrom: second}); err == nil {
		t.Fatal("create succeeded without audit")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM engagements`); got != 2 {
		t.Fatalf("engagements = %d", got)
	}
	if _, err := svc.Close(ctx, owner, orgID, e.ID, "2026-06-01"); err == nil {
		t.Fatal("close succeeded without audit")
	}
	name := "2026-02-01"
	if _, err := svc.Correct(ctx, owner, orgID, e.ID, Patch{ActiveFrom: optional.Value[string]{Set: true, Value: &name}}); err == nil {
		t.Fatal("correct succeeded without audit")
	}
	var stored time.Time
	var until *time.Time
	if err := pool.QueryRow(ctx, `SELECT active_from, active_until FROM engagements WHERE id=$1`, e.ID).Scan(&stored, &until); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(from) || until != nil {
		t.Fatalf("state leaked: %s %v", stored, until)
	}
	// Reopening a closed historical engagement must also roll back with no
	// audit event.
	if _, err := svc.Correct(ctx, owner, orgID, closed.ID, Patch{ActiveUntil: optional.Value[string]{Set: true}}); err == nil {
		t.Fatal("reopen succeeded without audit")
	}
	var closedUntil *time.Time
	if err := pool.QueryRow(ctx, `SELECT active_until FROM engagements WHERE id=$1`, closed.ID).Scan(&closedUntil); err != nil {
		t.Fatal(err)
	}
	if closedUntil == nil || !closedUntil.Equal(pastUntil) {
		t.Fatalf("closed engagement mutated: %v", closedUntil)
	}
}
