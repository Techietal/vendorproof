# VendorProof MVP — Acceptance criteria

> Phase 1 status: authentication, organization tenancy, membership roles, audit events,
> readiness, and migrations are implemented; see `docs/03-phase-1-foundation.md`.
> Phase 2 status: sites, contractors, and dated engagements (with overlap protection,
> lifecycle guards, reviewer read-only access, and audit) are implemented; see
> `docs/04-phase-2-contractor-sites.md`.
> Phase 3 status: versioned requirements, dated requirement assignments, and the
> monthly obligation generation engine are implemented; see
> `docs/05-phase-3-requirements-obligations.md`. Steps 6–10 below remain Phase 4 work
> (submissions, reviews, overdue tracking, and reporting).

## Personas

- **Organization owner**: manages organization memberships and configuration.
- **Manager**: manages sites, contractors, engagements, and requirement assignments.
- **Reviewer**: can review or reject submissions for authorized organizations.
- **Contractor uploader**: can upload only into requirements explicitly granted by a scoped invitation; cannot browse other contractors or organization data.

## End-to-end acceptance scenario

1. Owner registers one organization with two sites.
2. Manager registers three contractors; assigns one contractor to both sites.
3. Manager creates a versioned recurring monthly requirement. Its configured due day is the 10th of the following month.
4. Manager applies requirement versions to the appropriate engagements.
5. Scheduler generates one obligation per engagement and month. Repeating a run does not create duplicates.
6. Contractor receives an expiring scoped invitation, uploads a valid file, and sees it awaiting review.
7. Reviewer requests correction with a required reason; the contractor resubmits, preserving both document versions.
8. Reviewer accepts a clean scanned submission; the obligation now shows accepted evidence.
9. An unsubmitted obligation becomes overdue when its due date passes; a submitted file still awaiting internal review does not.
10. Administrator exports a month/site report with due dates, status, submission/review timestamps, and auditable evidence references.

## Phase 3 acceptance (implemented)

- Owner/manager can create a versioned requirement family with cadence, due day, and IANA timezone.
- New versions are immutable and numbered sequentially within a family.
- Manager can assign a requirement version to an engagement with an effective date range; overlapping assignments for the same family and engagement are rejected.
- Scheduler generates one obligation per engagement and month, with idempotency (re-running does not create duplicates).
- Obligations carry period, coverage window (partial-month intersection), due date (computed in the requirement's timezone), and lifecycle state.
- Reviewer can view obligations and assignments; cannot create or generate.
- The `phase3 generate-obligations` CLI runs generation with dry-run support.
- Migration 000004 adds Phase 3 tables and refuses downgrade when Phase 3 data exists.

- Owner/manager can create, read, update, deactivate, and reactivate sites and
  contractors; reviewers can only read.
- A contractor can be engaged at multiple sites; the same pair cannot hold overlapping
  periods, while adjacent periods are accepted.
- Closing keeps the historical engagement; a returning contractor gets a new record.
- Deactivation is refused while current or future engagements remain.
- Cross-tenant and indirect-reference probes return 404.
- Every mutation writes an audit event in the same transaction.

## Security gates before using real customer documents

- Authentication and RBAC enforced server-side for every business route.
- Negative tenant-isolation tests cover direct and indirect object references.
- Short-lived upload and download authorization; bucket denies public access.
- File limits, content validation, malware scanning, and quarantine are enforced.
- PII handling, retention/deletion, logging redaction, and backups documented.
- Audits have append-only application behavior and access controls.
- Database restore and dependency-failure recovery are rehearsed.

## Excluded from MVP

OCR, AI verification, automatic certification of legal compliance, WhatsApp messaging,
ERP integrations, payroll processing, and automated billing.
