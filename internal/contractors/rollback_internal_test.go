package contractors

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/audit"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/optional"
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

	c, err := svc.Create(ctx, owner, orgID, "ABC Security", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	svc.auditf = failAudit

	if _, err := svc.Create(ctx, owner, orgID, "Lost", ""); err == nil {
		t.Fatal("create succeeded without audit")
	}
	name := "Changed"
	if _, err := svc.Update(ctx, owner, orgID, c.ID, Patch{Name: optional.Value[string]{Set: true, Value: &name}}); err == nil {
		t.Fatal("update succeeded without audit")
	}
	if _, err := svc.Deactivate(ctx, owner, orgID, c.ID); err == nil {
		t.Fatal("deactivate succeeded without audit")
	}
	var stored, email string
	var active bool
	if err := pool.QueryRow(ctx, `SELECT name, contact_email, is_active FROM contractors WHERE id=$1`, c.ID).Scan(&stored, &email, &active); err != nil {
		t.Fatal(err)
	}
	if stored != "ABC Security" || email != "a@example.com" || !active {
		t.Fatalf("state leaked: %q %q %t", stored, email, active)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM contractors`); got != 1 {
		t.Fatalf("contractors = %d", got)
	}
}
