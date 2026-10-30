// Command phase3 performs operational Phase 3 functions: generating monthly
// obligations for a given organization and reporting month. It is intentionally
// separate from the API so it can be run as a scheduled job or ad-hoc operation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"vendorproof/internal/config"
	"vendorproof/internal/database"
	"vendorproof/internal/identity"
	"vendorproof/internal/obligations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "phase3:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || os.Args[1] != "generate-obligations" {
		return errors.New("usage: phase3 generate-obligations --org ID --year N --month N [--dry-run]")
	}

	fs := flag.NewFlagSet("generate-obligations", flag.ContinueOnError)
	orgID := fs.String("org", "", "organization ID (required)")
	year := fs.Int("year", time.Now().Year(), "reporting year")
	month := fs.Int("month", int(time.Now().Month()), "reporting month 1-12")
	dryRun := fs.Bool("dry-run", false, "preview without persisting")

	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	if *orgID == "" {
		return errors.New("--org is required")
	}
	oid, err := uuid.Parse(*orgID)
	if err != nil {
		return fmt.Errorf("invalid organization ID: %w", err)
	}
	if *month < 1 || *month > 12 {
		return errors.New("--month must be between 1 and 12")
	}

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	m, err := database.NewMigrator(url)
	if err != nil {
		return err
	}
	defer m.Close()

	// Ensure migrations are current.
	version, _, _ := m.Version()
	if version != 4 {
		if err := m.Up(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	svc := obligations.NewService(pool)
	result, err := svc.GenerateForMonth(ctx, identity.System(), oid, *year, *month, *dryRun)
	if err != nil {
		return fmt.Errorf("generate obligations: %w", err)
	}

	fmt.Printf("Period: %s to %s\n", result.PeriodStart.Format("2006-01-02"), result.PeriodEnd.Format("2006-01-02"))
	fmt.Printf("Created: %d, Existing: %d, Conflicts: %d, Skipped: %d\n",
		result.Created, result.Existing, result.Conflicts, result.Skipped)
	return nil
}
