package organizations

import (
	"context"
	"errors"
	"testing"

	"vendorproof/internal/audit"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/testutil"
)

func TestBootstrapRollsBackWhenAuditFails(t *testing.T) {
	pool := testutil.NewDB(t)
	service := NewService(pool, NewRepository(pool))
	service.auditf = func(context.Context, database.Querier, audit.Event) error {
		return errors.New("audit unavailable")
	}
	if _, _, err := service.Bootstrap(context.Background(), "Acme", "https://idp", "owner"); err == nil {
		t.Fatal("expected failure")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM organizations`); got != 0 {
		t.Fatalf("organization leaked: %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships`); got != 0 {
		t.Fatalf("membership leaked: %d", got)
	}
}

func TestUpdateRollsBackNameWhenAuditFails(t *testing.T) {
	pool := testutil.NewDB(t)
	service := NewService(pool, NewRepository(pool))
	org, _, err := service.Bootstrap(context.Background(), "Original", "https://idp", "owner")
	if err != nil {
		t.Fatal(err)
	}
	service.auditf = func(context.Context, database.Querier, audit.Event) error {
		return errors.New("audit unavailable")
	}
	if _, err := service.Update(context.Background(), identityFor("owner"), org.ID, "Changed"); err == nil {
		t.Fatal("expected failure")
	}
	var name string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM organizations WHERE id=$1`, org.ID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Original" {
		t.Fatalf("name = %q, not rolled back", name)
	}
}

func identityFor(subject string) identity.Identity {
	return identity.Identity{Issuer: "https://idp", Subject: subject}
}
