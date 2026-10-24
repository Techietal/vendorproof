package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrAlreadyInitialized means the ledger already records a version.
	ErrAlreadyInitialized = errors.New("database already has a migration version")
	// ErrIncompatibleBaseline means the schema does not match 000001.
	ErrIncompatibleBaseline = errors.New("database schema does not match the initial schema")
	// ErrConfirmationRequired means the operator did not attest the database.
	ErrConfirmationRequired = errors.New("baseline requires explicit --confirm")
)

// Baseline records version 1 for a database whose 000001 schema was applied
// manually before the migration ledger existed.
//
// It compares the live public schema (tables, columns, types, nullability,
// defaults, constraints, indexes, triggers, functions) against a golden
// manifest derived from 000001_initial.up.sql. Extensions, schemas other than
// public, ownership, grants, and collation details are outside the contract.
// The operator must pass --confirm; this does not replace validation.
//
// Baseline only writes the migration ledger via Force(1). It never changes the
// application schema or data, and refuses any existing version, dirty or not.
// It is not race-safe against a concurrent migration process; run it offline.
func Baseline(ctx context.Context, q Querier, m *Migrator, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	// Check the ledger directly. m.Version() creates schema_migrations when it
	// is absent, which would make a failed validation mutate the database.
	var ledgerExists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&ledgerExists); err != nil {
		return fmt.Errorf("inspect migration ledger: %w", err)
	}
	if ledgerExists {
		var version int64
		var dirty bool
		err := q.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
		switch {
		case err == nil:
			return fmt.Errorf("%w: version %d dirty=%t; run 'up' instead", ErrAlreadyInitialized, version, dirty)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read migration version: %w", err)
		}
	}

	want, err := loadExpectedManifest()
	if err != nil {
		return fmt.Errorf("load expected schema: %w", err)
	}
	got, err := captureManifest(ctx, q)
	if err != nil {
		return fmt.Errorf("inspect schema for baseline: %w", err)
	}
	if diffs := compareManifests(want, got); len(diffs) > 0 {
		return fmt.Errorf("%w: %s", ErrIncompatibleBaseline, summarize(diffs))
	}
	if err := m.Force(1); err != nil {
		return fmt.Errorf("record baseline: %w", err)
	}
	return nil
}
