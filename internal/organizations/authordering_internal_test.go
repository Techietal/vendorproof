package organizations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
	"vendorproof/internal/testutil"
)

const orderIssuer = "https://idp.test"

func orderSeed(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, identity.Identity) {
	t.Helper()
	orgID := uuid.New()
	owner := identity.Identity{Issuer: orderIssuer, Subject: "owner-" + orgID.String()[:8]}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, 'owner')`, uuid.New(), orgID, owner.Issuer, owner.Subject)
	return orgID, owner
}

func waitForOrgLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%SELECT id FROM organizations%'`).Scan(&waiting)
		if err == nil && waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update never waited on the organization lock")
}

// A revocation holding the organization lock must be visible to an Update that
// was already in flight; the update must not run with stale authorization.
func TestUpdateSerializesWithRevocation(t *testing.T) {
	pool := testutil.NewDB(t)
	service := NewService(pool, NewRepository(pool))
	orgID, owner := orderSeed(t, pool)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE memberships SET revoked_at=now()
		WHERE organization_id=$1 AND identity_subject=$2`, orgID, owner.Subject); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := service.Update(ctx, owner, orgID, "Changed")
		done <- err
	}()
	waitForOrgLockWait(t, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("update after revocation = %v, want not found", err)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id=$1`, orgID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Org" {
		t.Fatalf("organization changed by revoked owner: %q", name)
	}
}

// The authorization read and membership listing must share a snapshot so a
// revocation committing mid-request cannot make the response inconsistent.
func TestListMembershipsUsesConsistentSnapshot(t *testing.T) {
	pool := testutil.NewDB(t)
	service := NewService(pool, NewRepository(pool))
	orgID, owner := orderSeed(t, pool)
	ctx := context.Background()
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, 'reviewer', 'reviewer')`, uuid.New(), orgID, orderIssuer)

	var fired bool
	service.afterAuthorize = func() {
		fired = true
		// Commit on a separate connection, after authorization has read.
		testutil.MustExec(t, pool, `UPDATE memberships SET revoked_at=now()
			WHERE organization_id=$1 AND identity_subject='reviewer'`, orgID)
	}

	items, _, err := service.ListMemberships(ctx, owner, orgID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("race hook did not run")
	}
	// Repeatable-read must still see the reviewer that was active during auth.
	if len(items) != 2 {
		t.Fatalf("items = %d, want snapshot of 2", len(items))
	}

	service.afterAuthorize = nil
	items, _, err = service.ListMemberships(ctx, owner, orgID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items after revocation = %d, want 1", len(items))
	}
}

var _ = authz.Owner
