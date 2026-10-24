package database

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx5 database/sql driver

	"vendorproof/db/migrations"
)

// Migrator wraps the migration engine with a small, explicit API.
type Migrator struct {
	m    *migrate.Migrate
	conn *sql.DB
}

// NewMigrator opens a single-connection migration handle.
func NewMigrator(databaseURL string) (*Migrator, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}
	conn, err := sql.Open("pgx/v5", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open migration connection: %w", err)
	}
	conn.SetMaxOpenConns(1)

	driver, err := pgx.WithInstance(conn, &pgx.Config{})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("configure migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{m: m, conn: conn}, nil
}

func (m *Migrator) Close() error {
	srcErr, dbErr := m.m.Close()
	return errors.Join(srcErr, dbErr)
}

// Up applies every pending migration.
func (m *Migrator) Up() error {
	return ignoreNoChange(m.m.Up())
}

// Down reverts the most recent migration.
func (m *Migrator) Down() error {
	return ignoreNoChange(m.m.Steps(-1))
}

// Version reports the current version and dirty flag.
func (m *Migrator) Version() (uint, bool, error) {
	return m.m.Version()
}

// Force pins the recorded version without running SQL. It is used to baseline
// databases whose schema was applied manually before migration tracking existed.
func (m *Migrator) Force(version int) error {
	return m.m.Force(version)
}

func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
