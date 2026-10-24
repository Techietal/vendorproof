package database_test

import (
	"context"
	"errors"
	"strings"
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
	if err := database.Baseline(context.Background(), pool, m); err != nil {
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

func TestBaselineGuards(t *testing.T) {
	t.Run("uninitialized database", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		m, err := database.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := database.Baseline(context.Background(), pool, m); !errors.Is(err, database.ErrIncompatibleBaseline) {
			t.Fatalf("err = %v", err)
		}
		if _, _, err := m.Version(); err == nil {
			t.Fatal("baseline recorded a version on an empty database")
		}
	})

	t.Run("unrelated schema", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		testutil.MustExec(t, pool, `CREATE TABLE unrelated (id int)`)
		m, err := database.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := database.Baseline(context.Background(), pool, m); !errors.Is(err, database.ErrIncompatibleBaseline) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("already migrated", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		m, err := database.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := m.Up(); err != nil {
			t.Fatal(err)
		}
		if err := database.Baseline(context.Background(), pool, m); !errors.Is(err, database.ErrAlreadyInitialized) {
			t.Fatalf("err = %v", err)
		}
		if version, _, _ := m.Version(); version != 2 {
			t.Fatalf("version changed to %d", version)
		}
	})

	t.Run("already baselined", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		initial, err := migrations.FS.ReadFile("000001_initial.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), string(initial)); err != nil {
			t.Fatal(err)
		}
		m, err := database.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := m.Force(1); err != nil {
			t.Fatal(err)
		}
		if err := database.Baseline(context.Background(), pool, m); !errors.Is(err, database.ErrAlreadyInitialized) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDownPreservesRecordsWhenCompatible(t *testing.T) {
	url, pool := testutil.NewRawDB(t)
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	orgID := "11111111-1111-1111-1111-111111111111"
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, orgID)
	testutil.MustExec(t, pool, `INSERT INTO memberships (id, organization_id, identity_issuer, identity_subject, role)
		VALUES ('22222222-2222-2222-2222-222222222222', $1, 'issuer', 'subject', 'owner')`, orgID)

	if err := m.Down(); err != nil {
		t.Fatalf("compatible down: %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships`); got != 1 {
		t.Fatalf("membership rows = %d, want preserved", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name='memberships' AND column_name='identity_issuer'`); got != 0 {
		t.Fatal("identity_issuer still present")
	}
}

func TestDownRefusesDuplicateHistoryWithoutLosingData(t *testing.T) {
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

	err = m.Down()
	if err == nil || !strings.Contains(err.Error(), "cannot roll back 000002 losslessly") {
		t.Fatalf("down error = %v", err)
	}
	// Both rows and the newer schema survive the failed migration.
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE identity_subject='same'`); got != 2 {
		t.Fatalf("memberships after refused down = %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name='memberships' AND column_name='identity_issuer'`); got != 1 {
		t.Fatal("identity_issuer lost by refused down")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname='memberships_active_identity_unique'`); got != 1 {
		t.Fatal("active-membership index lost by refused down")
	}
	version, dirty, verr := m.Version()
	if verr != nil || dirty != true || (version != 1 && version != 2) {
		t.Fatalf("version=%d dirty=%t err=%v", version, dirty, verr)
	}

	// After an explicit merge the rollback can proceed.
	testutil.MustExec(t, pool, `DELETE FROM memberships WHERE id=$1`, revokedID)
	if err := m.Force(2); err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil {
		t.Fatalf("down after cleanup: %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships WHERE identity_subject='same'`); got != 1 {
		t.Fatalf("memberships after cleanup down = %d", got)
	}
}
