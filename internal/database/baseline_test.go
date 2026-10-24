package database_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/db/migrations"
	"vendorproof/internal/database"
	"vendorproof/internal/testutil"
)

func applyMigrationFile(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	raw, err := migrations.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), string(raw)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

func baselineMigrator(t *testing.T, url string) *database.Migrator {
	t.Helper()
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func baselineFixture(t *testing.T) (*pgxpool.Pool, *database.Migrator) {
	t.Helper()
	url, pool := testutil.NewRawDB(t)
	applyMigrationFile(t, pool, "000001_initial.up.sql")
	return pool, baselineMigrator(t, url)
}

func poolURL(pool *pgxpool.Pool) string { return pool.Config().ConnString() }

func hasVersion(t *testing.T, m *database.Migrator) bool {
	t.Helper()
	_, _, err := m.Version()
	return err == nil
}

func TestBaselineRequiresConfirmation(t *testing.T) {
	pool, m := baselineFixture(t)
	err := database.Baseline(context.Background(), pool, m, false)
	if !errors.Is(err, database.ErrConfirmationRequired) {
		t.Fatalf("err = %v", err)
	}
	if hasVersion(t, m) {
		t.Fatal("version recorded without confirmation")
	}
}

func TestBaselineRefusesPartialSchema(t *testing.T) {
	_, pool := testutil.NewRawDB(t)
	testutil.MustExec(t, pool, `CREATE TABLE organizations (id uuid PRIMARY KEY, name text NOT NULL)`)
	testutil.MustExec(t, pool, `CREATE TABLE memberships (id uuid PRIMARY KEY, organization_id uuid NOT NULL, identity_subject text NOT NULL)`)
	testutil.MustExec(t, pool, `CREATE TABLE audit_events (id uuid PRIMARY KEY)`)
	m := baselineMigrator(t, poolURL(pool))

	err := database.Baseline(context.Background(), pool, m, true)
	if !errors.Is(err, database.ErrIncompatibleBaseline) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "missing column") && !strings.Contains(err.Error(), "missing constraint") {
		t.Fatalf("expected structural detail, got %v", err)
	}
}

func TestBaselineRefusesMissingConstraint(t *testing.T) {
	pool, m := baselineFixture(t)
	testutil.MustExec(t, pool, `ALTER TABLE memberships DROP CONSTRAINT memberships_organization_id_identity_subject_key`)
	err := database.Baseline(context.Background(), pool, m, true)
	if !errors.Is(err, database.ErrIncompatibleBaseline) || !strings.Contains(err.Error(), "memberships_organization_id_identity_subject_key") {
		t.Fatalf("err = %v", err)
	}
}

func TestBaselineRefusesMissingIndex(t *testing.T) {
	pool, m := baselineFixture(t)
	testutil.MustExec(t, pool, `DROP INDEX obligations_due`)
	err := database.Baseline(context.Background(), pool, m, true)
	if !errors.Is(err, database.ErrIncompatibleBaseline) || !strings.Contains(err.Error(), "obligations_due") {
		t.Fatalf("err = %v", err)
	}
}

func TestBaselineRefusesChangedColumn(t *testing.T) {
	pool, m := baselineFixture(t)
	testutil.MustExec(t, pool, `ALTER TABLE organizations ALTER COLUMN name DROP NOT NULL`)
	err := database.Baseline(context.Background(), pool, m, true)
	if !errors.Is(err, database.ErrIncompatibleBaseline) || !strings.Contains(err.Error(), "not_null") {
		t.Fatalf("err = %v", err)
	}
}

func TestBaselineRefusesVersionTwoSchemaWithoutLedger(t *testing.T) {
	_, pool := testutil.NewRawDB(t)
	applyMigrationFile(t, pool, "000001_initial.up.sql")
	applyMigrationFile(t, pool, "000002_phase1_identity_and_invariants.up.sql")
	m := baselineMigrator(t, poolURL(pool))

	err := database.Baseline(context.Background(), pool, m, true)
	if !errors.Is(err, database.ErrIncompatibleBaseline) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "unexpected column") && !strings.Contains(err.Error(), "identity_issuer") {
		t.Fatalf("expected v2 difference, got %v", err)
	}
	if hasVersion(t, m) {
		t.Fatal("version recorded")
	}
}

func TestFailedBaselineHasNoSideEffects(t *testing.T) {
	pool, m := baselineFixture(t)
	testutil.MustExec(t, pool, `ALTER TABLE sites DROP CONSTRAINT sites_pkey`)
	schemaBefore := schemaSnapshot(t, pool)
	dataBefore := dataSnapshot(t, pool)

	if err := database.Baseline(context.Background(), pool, m, true); !errors.Is(err, database.ErrIncompatibleBaseline) {
		t.Fatalf("err = %v", err)
	}
	if after := schemaSnapshot(t, pool); after != schemaBefore {
		t.Fatal("schema mutated by failed baseline")
	}
	if after := dataSnapshot(t, pool); after != dataBefore {
		t.Fatal("data mutated by failed baseline")
	}
	// NewMigrator itself may create an empty ledger table; baseline must not
	// write a version row.
	if got := testutil.Count(t, pool, `SELECT count(*) FROM schema_migrations`); got != 0 {
		t.Fatalf("failed baseline recorded %d version rows", got)
	}
}

func TestBaselineIsIdempotentAndRefusesRepeats(t *testing.T) {
	pool, m := baselineFixture(t)
	if err := database.Baseline(context.Background(), pool, m, true); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, m, 1)
	if err := database.Baseline(context.Background(), pool, m, true); !errors.Is(err, database.ErrAlreadyInitialized) {
		t.Fatalf("second baseline = %v", err)
	}
	assertVersion(t, m, 1)
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, m, 2)
}
