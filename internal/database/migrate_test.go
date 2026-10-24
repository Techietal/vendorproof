package database_test

import (
	"context"
	"testing"

	"vendorproof/db/migrations"
	"vendorproof/internal/database"
	"vendorproof/internal/testutil"
)

func TestMigrationsUpDownUp(t *testing.T) {
	url, pool := testutil.NewRawDB(t)
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	version, dirty, err := m.Version()
	if err != nil || version != 2 || dirty {
		t.Fatalf("version=%d dirty=%t err=%v", version, dirty, err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname='public'`); got < 10 {
		t.Fatalf("tables = %d", got)
	}

	// Revert both migrations; the schema must disappear cleanly.
	if err := m.Down(); err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations'`); got != 0 {
		t.Fatalf("tables after full down = %d", got)
	}

	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name='memberships' AND column_name='identity_issuer'`); got != 1 {
		t.Fatalf("identity_issuer missing after re-up")
	}
}

func TestBaselineOfManuallyAppliedInitialSchema(t *testing.T) {
	url, pool := testutil.NewRawDB(t)
	initial, err := migrations.FS.ReadFile("000001_initial.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(initial)); err != nil {
		t.Fatalf("apply initial schema manually: %v", err)
	}
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Force(1); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	version, _, err := m.Version()
	if err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name='audit_events' AND column_name='actor_issuer'`); got != 1 {
		t.Fatalf("forward migration did not apply")
	}
}

func TestDownWithRevokedDuplicate(t *testing.T) {
	url, pool := testutil.NewRawDB(t)
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	var orgID, activeID, revokedID string
	orgID = "11111111-1111-1111-1111-111111111111"
	activeID = "22222222-2222-2222-2222-222222222222"
	revokedID = "33333333-3333-3333-3333-333333333333"
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role, revoked_at)
		VALUES ($1, $2, 'i', 'same', 'owner', now() - interval '1 hour')`, revokedID, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ($1, $2, 'i', 'same', 'owner')`, activeID, orgID)

	if err := m.Down(); err != nil {
		t.Fatalf("down with duplicate identity: %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE identity_subject='same'`); got != 1 {
		t.Fatalf("memberships after down = %d", got)
	}
}
