// Command migrate applies versioned SQL migrations with an explicit version
// ledger. It is intentionally separate from the API so replicas do not race.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"

	"vendorproof/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() == 0 {
		usage()
		return errors.New("a command is required")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	m, err := database.NewMigrator(url)
	if err != nil {
		return err
	}
	defer m.Close()

	switch cmd := flag.Arg(0); cmd {
	case "up":
		if err := m.Up(); err != nil {
			return err
		}
		return printVersion(m)
	case "down":
		if err := m.Down(); err != nil {
			return err
		}
		return printVersion(m)
	case "version":
		return printVersion(m)
	case "force":
		if flag.NArg() < 2 {
			return errors.New("usage: migrate force <version>")
		}
		version, err := strconv.Atoi(flag.Arg(1))
		if err != nil || version < 1 {
			return errors.New("version must be a positive integer")
		}
		if err := m.Force(version); err != nil {
			return err
		}
		fmt.Println("forced version", version)
		return nil
	case "baseline":
		// 000001 was applied manually before migration tracking existed.
		if err := m.Force(1); err != nil {
			return err
		}
		fmt.Println("baselined at version 1; run 'up' to apply forward migrations")
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func printVersion(m *database.Migrator) error {
	version, dirty, err := m.Version()
	if err != nil {
		return err
	}
	fmt.Printf("version %d dirty=%t\n", version, dirty)
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: migrate <command>

commands:
  up          apply all pending migrations
  down        revert one migration
  version     show the recorded version
  force N     record version N without executing SQL
  baseline    mark the manually applied initial schema as version 1

DATABASE_URL is required. Baseline an existing database before the first "up".
`)
}
