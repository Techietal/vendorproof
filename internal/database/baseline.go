package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
)

var (
	// ErrAlreadyInitialized means the ledger already records a version.
	ErrAlreadyInitialized = errors.New("database already has a migration version")
	// ErrIncompatibleBaseline means the schema is not the unmodified 000001.
	ErrIncompatibleBaseline = errors.New("database schema does not match the initial schema")
)

// Baseline records version 1 for a database whose 000001 schema was applied
// manually before the migration ledger existed. It refuses anything else.
func Baseline(ctx context.Context, q Querier, m *Migrator) error {
	version, _, err := m.Version()
	switch {
	case err == nil:
		return fmt.Errorf("%w: version %d; run 'up' instead", ErrAlreadyInitialized, version)
	case !errors.Is(err, migrate.ErrNilVersion):
		return fmt.Errorf("read migration version: %w", err)
	}

	var organizations, memberships, auditEvents, subject, issuer bool
	err = q.QueryRow(ctx, `
		SELECT
			to_regclass('public.organizations') IS NOT NULL,
			to_regclass('public.memberships') IS NOT NULL,
			to_regclass('public.audit_events') IS NOT NULL,
			EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name='memberships' AND column_name='identity_subject'),
			EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name='memberships' AND column_name='identity_issuer')`,
	).Scan(&organizations, &memberships, &auditEvents, &subject, &issuer)
	if err != nil {
		return fmt.Errorf("inspect schema for baseline: %w", err)
	}
	if !organizations || !memberships || !auditEvents || !subject {
		return fmt.Errorf("%w: expected organizations, memberships, audit_events, and memberships.identity_subject", ErrIncompatibleBaseline)
	}
	if issuer {
		return fmt.Errorf("%w: identity_issuer already exists; the database is at least 000002", ErrIncompatibleBaseline)
	}
	if err := m.Force(1); err != nil {
		return fmt.Errorf("record baseline: %w", err)
	}
	return nil
}
