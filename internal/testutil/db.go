// Package testutil provides disposable PostgreSQL databases for integration
// tests. Every call creates and drops its own database; persistent data is
// never touched.
package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/internal/config"
	"vendorproof/internal/database"
)

const defaultAdminURL = "postgres://vendorproof:local-dev-password@localhost:5432/vendorproof?sslmode=disable"

func adminURL() string {
	if raw := os.Getenv("TEST_DATABASE_URL"); raw != "" {
		return raw
	}
	return defaultAdminURL
}

// databaseRequired turns connection failures into failures instead of skips.
// Acceptance runs set REQUIRE_DATABASE=1.
func databaseRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("REQUIRE_DATABASE"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// NewRawDB creates an empty database (without migrations) and registers
// cleanup. It returns the URL and an open pool.
func NewRawDB(t testing.TB) (string, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		if databaseRequired() {
			t.Fatalf("REQUIRE_DATABASE=1 but PostgreSQL is unavailable: %v (set TEST_DATABASE_URL)", err)
		}
		t.Skipf("PostgreSQL unavailable (%v); set TEST_DATABASE_URL or REQUIRE_DATABASE=1", err)
	}
	name := "vp_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		admin.Close(context.Background())
		t.Fatalf("create test database: %v", err)
	}

	target := databaseURL(t, adminURL(), name)
	pool, err := database.Open(ctx, config.Database{
		URL: target, MaxConns: 10, MinConns: 1,
		ConnectTimeout: 5 * time.Second, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer dropCancel()
		_, _ = admin.Exec(dropCtx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
		_ = admin.Close(dropCtx)
	})
	return target, pool
}

// NewDB creates a migrated, empty database and registers cleanup.
func NewDB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	target, pool := NewRawDB(t)
	migrator, err := database.NewMigrator(target)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	if err := migrator.Up(); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	_ = migrator.Close()
	return pool
}

func databaseURL(t testing.TB, raw, name string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// MustExec runs a statement or fails the test.
func MustExec(t testing.TB, pool *pgxpool.Pool, sql string, args ...any) pgconn.CommandTag {
	t.Helper()
	tag, err := pool.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
	return tag
}

// Count returns the integer result of a scalar query.
func Count(t testing.TB, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// WaitForLockWait blocks until a backend other than this pool's current
// connection is waiting on a lock for a query containing match.
func WaitForLockWait(t testing.TB, pool *pgxpool.Pool, match string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE $1
			  AND pid <> pg_backend_pid()`, "%"+match+"%").Scan(&waiting)
		if err == nil && waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no backend waited on a lock matching %q", match)
}
