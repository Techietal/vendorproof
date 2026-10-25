package sites

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/audit"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/testutil"
)

func rollbackSeed(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, identity.Identity) {
	t.Helper()
	orgID := uuid.New()
	owner := identity.Identity{Issuer: "https://idp", Subject: "owner"}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, 'https://idp', 'owner', 'owner')`, uuid.New(), orgID)
	return orgID, owner
}

func failAudit(context.Context, database.Querier, audit.Event) error {
	return errors.New("audit unavailable")
}

func TestMutationsRollBackWhenAuditFails(t *testing.T) {
	pool := testutil.NewDB(t)
	svc := NewService(pool)
	orgID, owner := rollbackSeed(t, pool)
	ctx := context.Background()

	site, err := svc.Create(ctx, owner, orgID, "Factory A")
	if err != nil {
		t.Fatal(err)
	}
	svc.auditf = failAudit

	if _, err := svc.Create(ctx, owner, orgID, "Lost"); err == nil {
		t.Fatal("create succeeded without audit")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM sites`); got != 1 {
		t.Fatalf("sites = %d", got)
	}
	name := "Changed"
	if _, err := svc.Update(ctx, owner, orgID, site.ID, &name); err == nil {
		t.Fatal("update succeeded without audit")
	}
	if _, err := svc.Deactivate(ctx, owner, orgID, site.ID); err == nil {
		t.Fatal("deactivate succeeded without audit")
	}
	var stored string
	var active bool
	if err := pool.QueryRow(ctx, `SELECT name, is_active FROM sites WHERE id=$1`, site.ID).Scan(&stored, &active); err != nil {
		t.Fatal(err)
	}
	if stored != "Factory A" || !active {
		t.Fatalf("state leaked: %q active=%t", stored, active)
	}
}
