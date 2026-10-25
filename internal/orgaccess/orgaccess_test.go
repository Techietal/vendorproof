package orgaccess_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/apperr"
	"vendorproof/internal/authz"
	"vendorproof/internal/identity"
	"vendorproof/internal/orgaccess"
	"vendorproof/internal/testutil"
)

const issuer = "https://idp.test"

func seed(t *testing.T, pool *pgxpool.Pool, role authz.Role) (uuid.UUID, identity.Identity) {
	t.Helper()
	orgID := uuid.New()
	actor := identity.Identity{Issuer: issuer, Subject: "user-" + orgID.String()[:8]}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, $3, $4, $5)`, uuid.New(), orgID, actor.Issuer, actor.Subject, role)
	return orgID, actor
}

func TestAuthorize(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	orgID, actor := seed(t, pool, authz.Manager)

	access, err := orgaccess.Authorize(ctx, pool, orgID, actor, authz.CanManageSites)
	if err != nil || access.Role != authz.Manager {
		t.Fatalf("access = %+v err %v", access, err)
	}
	if _, err := orgaccess.Authorize(ctx, pool, orgID, actor, func(authz.Role) bool { return false }); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("forbidden = %v", err)
	}
	stranger := identity.Identity{Issuer: issuer, Subject: "stranger"}
	if _, err := orgaccess.Authorize(ctx, pool, orgID, stranger, authz.CanViewSites); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("stranger = %v", err)
	}
	if _, err := orgaccess.Authorize(ctx, pool, uuid.New(), actor, authz.CanViewSites); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing org = %v", err)
	}
}

func TestRevokedMembershipIsInvisible(t *testing.T) {
	pool := testutil.NewDB(t)
	orgID, actor := seed(t, pool, authz.Owner)
	testutil.MustExec(t, pool, `UPDATE memberships SET revoked_at=now() WHERE organization_id=$1`, orgID)
	if _, err := orgaccess.Authorize(context.Background(), pool, orgID, actor, authz.CanViewSites); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("revoked = %v", err)
	}
}

func TestLockAndAuthorize(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	orgID, actor := seed(t, pool, authz.Owner)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := orgaccess.LockAndAuthorize(ctx, tx, orgID, actor, authz.CanManageContractors); err != nil {
		t.Fatal(err)
	}
	if _, err := orgaccess.LockAndAuthorize(ctx, tx, uuid.New(), actor, authz.CanManageContractors); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("missing org = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
