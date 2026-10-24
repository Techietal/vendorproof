# VendorProof — backend starter (development codename)

**Status: initial engineering scaffold, not a deployable SaaS product.**
The name VendorProof is already in use by other companies and requires rebranding before launch.

## Implemented and testable

- Standard-library Go HTTP server: `GET /health/live`
- Pure monthly obligation generator with reporting-period and due-date semantics
- Derived contractor-overdue state (does not blame contractors for internal review delays)
- Unit tests for leap years, year boundaries, rule intervals, validation, and overdue state
- Draft PostgreSQL schema for multi-tenant contractor-document workflow
- Local-only PostgreSQL Compose configuration

## Not implemented

Authentication, tenant authorization, PostgreSQL access/repositories, data migrations runner,
contractor APIs, uploads, malware scanning, reviews, reminders, email delivery, frontend,
auditing enforcement, and deployment automation. **Do not connect real customer data.**

## Run checks

Requires Go 1.23+.

```sh
go test ./...
go run ./cmd/api
# In a second shell: curl http://localhost:8080/health/live
```

Local Postgres (requires Docker Compose, not needed for the unit tests):

```sh
docker compose up -d db
```

The migration is drafted but not automatically applied. Apply it with your chosen migration
runner only after reviewing it. PostgreSQL runtime execution has not yet been verified.

## First implementation milestone

1. Add OIDC middleware and organization-scoped RBAC.
2. Add pgx/sqlc repositories and vetted migrations; implement tenant-scoped site,
   contractor, and engagement operations.
3. Add requirement-version configuration and transactional obligation generation
   using a database `UNIQUE` constraint for safe retries.
4. Add submission workflow and scanned/private object storage.
5. Add reminders through a transactional outbox and a durable worker.
6. Add audit reporting, frontend, and launch security review.

## Domain design notes

- An `engagement` connects one contractor to one site.
- Each obligation is tenant + engagement + requirement version + period.
- Reporting periods are half-open `[period_start, period_end)`.
- `due_day` for a monthly requirement means a day in the following calendar month,
  clamped to that month's last date; due time is 17:00 local. This is a **product default**,
  not a statutory due date.
- Rule versions must be snapshotted into obligations; never silently change an existing
  obligation's rules when an administrator edits a requirement.
- A submission awaiting internal review must not be marked contractor-overdue.
- File approval requires successful malware scan and an authorized human decision.
- The schema is a design baseline, not a validated production migration.
