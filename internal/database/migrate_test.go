package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"vendorproof/db/migrations"
	"vendorproof/internal/database"
	"vendorproof/internal/testutil"
)

func migratedDB(t *testing.T) (*pgxpool.Pool, *database.Migrator) {
	t.Helper()
	url, pool := testutil.NewRawDB(t)
	m, err := database.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	return pool, m
}

func assertVersion(t *testing.T, m *database.Migrator, want uint) {
	t.Helper()
	version, dirty, err := m.Version()
	if err != nil || version != want || dirty {
		t.Fatalf("version=%d dirty=%t err=%v, want version %d clean", version, dirty, err, want)
	}
}

func mustRefuse(t *testing.T, m *database.Migrator, wantTable string) {
	t.Helper()
	err := m.Down()
	if err == nil {
		t.Fatal("downgrade succeeded on a populated database")
	}
	if !strings.Contains(err.Error(), "business data exists") || !strings.Contains(err.Error(), wantTable) {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

func columnExists(t *testing.T, pool *pgxpool.Pool, table, column string) bool {
	t.Helper()
	return testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, table, column) == 1
}

func v2SchemaIntact(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, col := range [][2]string{
		{"memberships", "identity_issuer"}, {"memberships", "revoked_at"},
		{"memberships", "updated_at"}, {"reviews", "identity_issuer"},
		{"audit_events", "actor_issuer"},
	} {
		if !columnExists(t, pool, col[0], col[1]) {
			t.Fatalf("v2 column %s.%s missing", col[0], col[1])
		}
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_indexes WHERE indexname='memberships_active_identity_unique'`); got != 1 {
		t.Fatal("active membership index missing")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname='audit_events_no_update'`); got != 1 {
		t.Fatal("audit append-only trigger missing")
	}
}

func insertOrganization(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, id)
	return id
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, issuer, subject, role string, revoked *time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	testutil.MustExec(t, pool, `INSERT INTO memberships
		(id, organization_id, identity_issuer, identity_subject, role, revoked_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, orgID, issuer, subject, role, revoked)
	return id
}

func insertAudit(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	testutil.MustExec(t, pool, `INSERT INTO audit_events
		(id, organization_id, actor_issuer, actor_subject, action, resource_type, resource_id, details)
		VALUES ($1, $2, 'https://issuer.example', 'actor-1', 'organization.updated', 'organization', $2, '{"fields":["name"]}')`,
		uuid.New(), orgID)
}

// A1: a genuinely empty database can round-trip 000002 and then be removed
// entirely.
func TestEmptyDatabaseRollbackRoundTrip(t *testing.T) {
	pool, m := migratedDB(t)
	assertVersion(t, m, 2)

	if err := m.Down(); err != nil {
		t.Fatalf("empty down: %v", err)
	}
	assertVersion(t, m, 1)
	if columnExists(t, pool, "memberships", "identity_issuer") || columnExists(t, pool, "audit_events", "actor_issuer") {
		t.Fatal("v2 columns remain after empty down")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_constraint WHERE conname='memberships_organization_id_identity_subject_key'`); got != 1 {
		t.Fatal("v1 membership uniqueness missing")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_constraint WHERE conname='memberships_role_check'`); got != 1 {
		t.Fatal("v1 role check missing")
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_constraint WHERE contype='f'`); got < 10 {
		t.Fatalf("v1 foreign keys = %d", got)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname='audit_events_no_update'`); got != 0 {
		t.Fatal("v2 trigger remains after down")
	}

	if err := m.Up(); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	assertVersion(t, m, 2)
	if err := m.Down(); err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil {
		t.Fatalf("v1 down: %v", err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations'`); got != 0 {
		t.Fatalf("tables after full down = %d", got)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("final up: %v", err)
	}
	assertVersion(t, m, 2)
	// The reapplied schema is usable.
	insertOrganization(t, pool)
}

// A2: one active, issuer-qualified membership without duplicates still blocks
// rollback so the issuer is not lost.
func TestRollbackRefusedForActiveMembership(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	membershipID := insertMembership(t, pool, orgID, "https://provider-a.example", "user-1", "owner", nil)

	mustRefuse(t, m, "organizations")
	v2SchemaIntact(t, pool)

	var issuer, subject, role, name string
	if err := pool.QueryRow(context.Background(), `SELECT identity_issuer, identity_subject, role FROM memberships WHERE id=$1`, membershipID).
		Scan(&issuer, &subject, &role); err != nil {
		t.Fatal(err)
	}
	if issuer != "https://provider-a.example" || subject != "user-1" || role != "owner" {
		t.Fatalf("membership changed: %s %s %s", issuer, subject, role)
	}
	if err := pool.QueryRow(context.Background(), `SELECT name FROM organizations WHERE id=$1`, orgID).Scan(&name); err != nil || name != "Org" {
		t.Fatalf("organization changed: %q %v", name, err)
	}
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships`); got != 1 {
		t.Fatalf("rows = %d", got)
	}
}

// A3: the primary regression — a single revoked membership must not silently
// become an active v1 row.
func TestRollbackRefusedForRevokedMembership(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	revokedAt := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	membershipID := insertMembership(t, pool, orgID, "https://provider-a.example", "user-123", "reviewer", &revokedAt)

	mustRefuse(t, m, "memberships")
	v2SchemaIntact(t, pool)

	var stored time.Time
	var issuer, subject, role string
	if err := pool.QueryRow(context.Background(), `SELECT revoked_at, identity_issuer, identity_subject, role
		FROM memberships WHERE id=$1`, membershipID).Scan(&stored, &issuer, &subject, &role); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(revokedAt) {
		t.Fatalf("revoked_at = %s, want %s", stored, revokedAt)
	}
	if issuer != "https://provider-a.example" || subject != "user-123" || role != "reviewer" {
		t.Fatalf("membership changed: %s %s %s", issuer, subject, role)
	}
}

// A4: identical subjects from different issuers remain distinguishable.
func TestRollbackRefusedForCrossIssuerSubjects(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	first := insertMembership(t, pool, orgID, "https://provider-a.example", "same", "owner", nil)
	second := insertMembership(t, pool, orgID, "https://provider-b.example", "same", "reviewer", nil)

	mustRefuse(t, m, "memberships")
	v2SchemaIntact(t, pool)

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM memberships WHERE identity_subject='same'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rows = %d err=%v", count, err)
	}
	for id, want := range map[uuid.UUID]string{first: "https://provider-a.example", second: "https://provider-b.example"} {
		var issuer string
		if err := pool.QueryRow(context.Background(), `SELECT identity_issuer FROM memberships WHERE id=$1`, id).Scan(&issuer); err != nil || issuer != want {
			t.Fatalf("issuer %s = %q err=%v", id, issuer, err)
		}
	}
}

// A5: audit provenance and its append-only protection survive refusal.
func TestRollbackRefusedForAuditHistory(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	insertAudit(t, pool, orgID)

	mustRefuse(t, m, "audit_events")
	v2SchemaIntact(t, pool)

	var actorIssuer, actorSubject, action, resourceType string
	var resourceID uuid.UUID
	var createdAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT actor_issuer, actor_subject, action, resource_type, resource_id, created_at
		FROM audit_events`).Scan(&actorIssuer, &actorSubject, &action, &resourceType, &resourceID, &createdAt); err != nil {
		t.Fatal(err)
	}
	if actorIssuer != "https://issuer.example" || actorSubject != "actor-1" || action != "organization.updated" ||
		resourceType != "organization" || resourceID != orgID || createdAt.IsZero() {
		t.Fatalf("audit row changed: %s %s %s %s %s %s", actorIssuer, actorSubject, action, resourceType, resourceID, createdAt)
	}
}

type reviewGraph struct {
	org, site, contractor, engagement, version, obligation, document, submission, review uuid.UUID
}

func insertReviewGraph(t *testing.T, pool *pgxpool.Pool) reviewGraph {
	t.Helper()
	g := reviewGraph{
		org: uuid.New(), site: uuid.New(), contractor: uuid.New(), engagement: uuid.New(),
		version: uuid.New(), obligation: uuid.New(), document: uuid.New(),
		submission: uuid.New(), review: uuid.New(),
	}
	testutil.MustExec(t, pool, `INSERT INTO organizations (id, name) VALUES ($1, 'Org')`, g.org)
	testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name) VALUES ($1, $2, 'Site')`, g.site, g.org)
	testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name) VALUES ($1, $2, 'Contractor')`, g.contractor, g.org)
	testutil.MustExec(t, pool, `INSERT INTO engagements (id, organization_id, contractor_id, site_id, active_from)
		VALUES ($1, $2, $3, $4, '2026-01-01')`, g.engagement, g.org, g.contractor, g.site)
	testutil.MustExec(t, pool, `INSERT INTO requirement_versions (id, organization_id, requirement_key, version, name, cadence, due_day, effective_from)
		VALUES ($1, $2, $3, 1, 'Requirement', 'monthly', 10, '2026-01-01')`, g.version, g.org, uuid.New())
	testutil.MustExec(t, pool, `INSERT INTO obligations (id, organization_id, engagement_id, requirement_version_id, period_start, period_end, due_at)
		VALUES ($1, $2, $3, $4, '2026-01-01', '2026-02-01', now())`, g.obligation, g.org, g.engagement, g.version)
	testutil.MustExec(t, pool, `INSERT INTO documents (id, organization_id, object_key, file_name, content_type, size_bytes)
		VALUES ($1, $2, $3, 'file.pdf', 'application/pdf', 10)`, g.document, g.org, "key-"+g.document.String())
	testutil.MustExec(t, pool, `INSERT INTO submissions (id, organization_id, obligation_id, document_id, attempt)
		VALUES ($1, $2, $3, $4, 1)`, g.submission, g.org, g.obligation, g.document)
	testutil.MustExec(t, pool, `INSERT INTO reviews (id, organization_id, submission_id, identity_issuer, identity_subject, decision, notes)
		VALUES ($1, $2, $3, 'https://reviewer.example', 'reviewer-7', 'needs_correction', 'missing page')`, g.review, g.org, g.submission)
	return g
}

// A6: a full realistic FK graph including a review preserves reviewer
// provenance and decisions.
func TestRollbackRefusedForReviewHistory(t *testing.T) {
	pool, m := migratedDB(t)
	g := insertReviewGraph(t, pool)

	mustRefuse(t, m, "reviews")
	v2SchemaIntact(t, pool)

	var issuer, subject, decision, notes string
	var reviewedAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT identity_issuer, identity_subject, decision, notes, reviewed_at
		FROM reviews WHERE id=$1`, g.review).Scan(&issuer, &subject, &decision, &notes, &reviewedAt); err != nil {
		t.Fatal(err)
	}
	if issuer != "https://reviewer.example" || subject != "reviewer-7" || decision != "needs_correction" ||
		notes != "missing page" || reviewedAt.IsZero() {
		t.Fatalf("review changed: %s %s %s %s %s", issuer, subject, decision, notes, reviewedAt)
	}
	for table, id := range map[string]uuid.UUID{
		"submissions": g.submission, "documents": g.document, "obligations": g.obligation,
		"engagements": g.engagement,
	} {
		if got := testutil.Count(t, pool, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id=$1`, table), id); got != 1 {
			t.Fatalf("%s row lost", table)
		}
	}
}

func schemaSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out strings.Builder
	for _, query := range []string{
		`SELECT table_name || '.' || column_name || ':' || data_type || ':' || is_nullable || ':' || coalesce(column_default, '')
		 FROM information_schema.columns WHERE table_schema='public' AND table_name <> 'schema_migrations' ORDER BY 1`,
		`SELECT c.relname || ':' || con.conname || ':' || con.contype::text || ':' || pg_get_constraintdef(con.oid)
		 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname='public' ORDER BY 1`,
		`SELECT indexname || ':' || indexdef FROM pg_indexes WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY 1`,
		`SELECT tgname FROM pg_trigger WHERE NOT tgisinternal ORDER BY 1`,
	} {
		rows, err := pool.Query(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
		rows.Close()
	}
	return out.String()
}

func dataSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"organizations", "memberships", "audit_events"} {
		var rows string
		if err := pool.QueryRow(context.Background(),
			fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id)::text, '[]') FROM %s x`, table)).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		out.WriteString(table + ":" + rows + "\n")
	}
	return out.String()
}

// A7: a refused rollback is atomic for business schema and data. Only the
// migration ledger's dirty flag may change.
func TestRefusedRollbackIsAtomic(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	insertMembership(t, pool, orgID, "https://provider.example", "user-9", "owner", &revokedAt)
	insertAudit(t, pool, orgID)

	schemaBefore := schemaSnapshot(t, pool)
	dataBefore := dataSnapshot(t, pool)
	mustRefuse(t, m, "business data exists")

	if after := schemaSnapshot(t, pool); after != schemaBefore {
		t.Fatal("schema changed after refused rollback")
	}
	if after := dataSnapshot(t, pool); after != dataBefore {
		t.Fatal("business data changed after refused rollback")
	}
	version, dirty, err := m.Version()
	if err != nil || !dirty || (version != 1 && version != 2) {
		t.Fatalf("ledger version=%d dirty=%t err=%v", version, dirty, err)
	}
}

// A8: documented manual recovery for the dirty ledger, without downgrading.
func TestRecoveryFromRefusedRollback(t *testing.T) {
	pool, m := migratedDB(t)
	orgID := insertOrganization(t, pool)
	insertMembership(t, pool, orgID, "https://provider.example", "user-1", "owner", nil)

	mustRefuse(t, m, "organizations")
	version, dirty, err := m.Version()
	if err != nil || !dirty || (version != 1 && version != 2) {
		t.Fatalf("ledger version=%d dirty=%t err=%v", version, dirty, err)
	}
	v2SchemaIntact(t, pool)

	// Operator verifies the intact v2 schema, then repairs only bookkeeping.
	if err := m.Force(2); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, m, 2)
	v2SchemaIntact(t, pool)
	if got := testutil.Count(t, pool, `SELECT count(*) FROM memberships`); got != 1 {
		t.Fatalf("rows = %d", got)
	}
	// Still populated, so a clean second attempt must refuse again.
	mustRefuse(t, m, "organizations")
}

// A9: every other populated business table is named in the refusal.
func TestRollbackRefusedForOtherBusinessTables(t *testing.T) {
	cases := map[string]func(*testing.T, *pgxpool.Pool){
		"sites": func(t *testing.T, pool *pgxpool.Pool) {
			org := insertOrganization(t, pool)
			testutil.MustExec(t, pool, `INSERT INTO sites (id, organization_id, name) VALUES ($1, $2, 'S')`, uuid.New(), org)
		},
		"contractors": func(t *testing.T, pool *pgxpool.Pool) {
			org := insertOrganization(t, pool)
			testutil.MustExec(t, pool, `INSERT INTO contractors (id, organization_id, name) VALUES ($1, $2, 'C')`, uuid.New(), org)
		},
		"documents": func(t *testing.T, pool *pgxpool.Pool) {
			org := insertOrganization(t, pool)
			testutil.MustExec(t, pool, `INSERT INTO documents (id, organization_id, object_key, file_name, content_type, size_bytes)
				VALUES ($1, $2, $3, 'f', 'text/plain', 1)`, uuid.New(), org, "k"+uuid.NewString())
		},
		"notification_deliveries": func(t *testing.T, pool *pgxpool.Pool) {
			org := insertOrganization(t, pool)
			testutil.MustExec(t, pool, `INSERT INTO notification_deliveries (id, organization_id, event_key, destination, status)
				VALUES ($1, $2, 'e', 'd', 'queued')`, uuid.New(), org)
		},
		"requirement_versions": func(t *testing.T, pool *pgxpool.Pool) {
			org := insertOrganization(t, pool)
			testutil.MustExec(t, pool, `INSERT INTO requirement_versions (id, organization_id, requirement_key, version, name, cadence, due_day, effective_from)
				VALUES ($1, $2, $3, 1, 'R', 'monthly', 10, '2026-01-01')`, uuid.New(), org, uuid.New())
		},
	}
	for table, insert := range cases {
		t.Run(table, func(t *testing.T) {
			pool, m := migratedDB(t)
			insert(t, pool)
			mustRefuse(t, m, table)
			v2SchemaIntact(t, pool)
			if got := testutil.Count(t, pool, fmt.Sprintf(`SELECT count(*) FROM %s`, table)); got != 1 {
				t.Fatalf("rows in %s = %d", table, got)
			}
		})
	}
}

// Baseline tests live below; rollback tests above.

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
	if err := database.Baseline(context.Background(), pool, m, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, m, 2)
	if got := testutil.Count(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name='audit_events' AND column_name='actor_issuer'`); got != 1 {
		t.Fatalf("forward migration did not apply")
	}
}

func TestBaselineGuards(t *testing.T) {
	newMigrator := func(t *testing.T, url string) *database.Migrator {
		t.Helper()
		m, err := database.NewMigrator(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		return m
	}

	t.Run("uninitialized database", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		m := newMigrator(t, url)
		if err := database.Baseline(context.Background(), pool, m, true); !errors.Is(err, database.ErrIncompatibleBaseline) {
			t.Fatalf("err = %v", err)
		}
		if _, _, err := m.Version(); err == nil {
			t.Fatal("baseline recorded a version on an empty database")
		}
	})

	t.Run("unrelated schema", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		testutil.MustExec(t, pool, `CREATE TABLE unrelated (id int)`)
		m := newMigrator(t, url)
		if err := database.Baseline(context.Background(), pool, m, true); !errors.Is(err, database.ErrIncompatibleBaseline) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("already migrated", func(t *testing.T) {
		url, pool := testutil.NewRawDB(t)
		m := newMigrator(t, url)
		if err := m.Up(); err != nil {
			t.Fatal(err)
		}
		if err := database.Baseline(context.Background(), pool, m, true); !errors.Is(err, database.ErrAlreadyInitialized) {
			t.Fatalf("err = %v", err)
		}
		assertVersion(t, m, 2)
	})
}
