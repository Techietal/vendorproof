// Command admin performs local administrative operations. It talks directly to
// PostgreSQL and is not exposed over HTTP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"vendorproof/internal/config"
	"vendorproof/internal/database"
	"vendorproof/internal/organizations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || os.Args[1] != "bootstrap-org" {
		return errors.New("usage: admin bootstrap-org --name NAME --issuer ISSUER --subject SUBJECT")
	}
	fs := flag.NewFlagSet("bootstrap-org", flag.ContinueOnError)
	name := fs.String("name", "", "organization name (required)")
	issuer := fs.String("issuer", "", "verified OIDC issuer of the first owner (required)")
	subject := fs.String("subject", "", "verified OIDC subject of the first owner (required)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	dbCfg := config.Database{URL: url, MaxConns: 2, MinConns: 1,
		ConnectTimeout: 5 * time.Second, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	service := organizations.NewService(pool, organizations.NewRepository(pool))
	org, membership, err := service.Bootstrap(ctx, *name, *issuer, *subject)
	if err != nil {
		return err
	}
	out := map[string]string{
		"organizationId": org.ID.String(),
		"membershipId":   membership.ID.String(),
		"name":           org.Name,
		"ownerIssuer":    membership.Identity.Issuer,
		"ownerSubject":   membership.Identity.Subject,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
