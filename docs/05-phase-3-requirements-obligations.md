# Phase 3: Requirements & Monthly Obligations

## Overview

Phase 3 introduces **versioned requirements**, **dated requirement assignments**, and a
**monthly obligation generation engine** on top of Phase 2's contractor-site-engagement model.

When a requirement is assigned to an engagement, the generation engine produces one
**obligation** per reporting month per (engagement, requirement family). Each obligation
captures the evidence-collection period, due date, and lifecycle state. Obligations feed
Phase 4's submission and review workflows.

---

## Domain Model

### Requirement Families & Versions

| Concept | Description |
|---------|-------------|
| **Requirement (family)** | A stable logical concept, e.g. "Monthly Attendance Register". Identified by a UUID `requirement_key`. |
| **RequirementVersion** | An immutable snapshot of scheduling rules effective from a date. Cadence, due day, timezone, and effective range are frozen at publication time. |

**Key invariants:**
- Versions are **immutable** once created. New rules get a new version row.
- A version is tied to exactly one family via `requirement_key`.
- No strict foreign key constraint from `requirement_versions.requirement_key` to `requirements.id` — the migration backfills `requirement_key` from existing UUIDs and the service layer enforces referential integrity. This avoids breaking existing test inserts that pre-date Phase 3.

### Requirement Assignments

Assignments connect a published version to an engagement with an explicit
effective date range.

```
requirement_assignments
┌─────────────────────────────────────────┐
│ id              uuid  PK                │
│ organization_id uuid  FK→organizations   │
│ engagement_id   uuid  FK→engagements    │
│ requirement_key uuid  (denormalized)    │
│ requirement_version_id uuid FK→req_vers │
│ effective_from    date (inclusive)      │
│ effective_until   date (exclusive, NULL)│
│ assigned_at       timestamptz           │
└─────────────────────────────────────────┘
```

**Overlap protection:** An exclusion constraint
(`requirement_assignments_no_overlap`) prevents overlapping assignment windows for
the same engagement and requirement family:
```sql
EXCLUDE USING gist (
    organization_id WITH =,
    engagement_id WITH =,
    requirement_key WITH =,
    daterange(effective_from, effective_until, '[)') WITH &&
)
```
This relies on the `btree_gist` extension installed by migration 000003.

### Obligations

Each obligation represents one reporting period's evidence requirement for a specific
engagement and requirement family.

```
obligations
┌──────────────────────────────────────────┐
│ id              uuid  PK                 │
│ organization_id uuid  FK→organizations    │
│ engagement_id   uuid  FK→engagements     │
│ requirement_key uuid                     │
│ requirement_version_id uuid             │
│ period_start    date  (month start)      │
│ period_end      date  (month end)        │
│ coverage_start  date  (actual start)     │
│ coverage_end    date  (actual end)       │
│ due_at          timestamptz              │
│ state           text  (default: 'awaiting_submission') │
│ created_at      timestamptz              │
└──────────────────────────────────────────┘
```

**Family-level uniqueness:** The constraint
`obligations_family_month_key` on `(organization_id, engagement_id, requirement_key, period_start)`
ensures idempotent generation — running the engine twice for the same month produces no
duplicates.

---

## Monthly Obligation Engine

### Coverage Intersection

An obligation's `coverage_start` and `coverage_end` represent the actual date range
that the assignment was active during the reporting period. The engine computes the
intersection of:

1. The reporting month (`period_start` to `period_end`)
2. The assignment's effective range (`effective_from` to `effective_until`)
3. The engagement's active range (`active_from` to `active_until`)
4. The requirement version's effective range (`version.effective_from` to `version.effective_until`)

If the intersection is empty, the obligation is skipped (not generated).

### Timezone Handling

- Each requirement version stores an IANA timezone in `reporting_timezone`.
- Dates (not instants) are used for period and coverage boundaries.
- The `due_at` timestamp is calculated in the requirement's timezone (5 PM on the due day
  in that timezone), then stored as a UTC `timestamptz`.
- No conversion to UTC instants is performed; calendar dates are interpreted in the
  stored timezone.

### Due Date Calculation

- Due day is the **following** calendar month after the reporting period.
- If the due day exceeds the last day of that month, it is clamped (e.g., day 31 in
  February becomes February 28 or 29).
- The time component is 17:00 (5 PM) in the requirement's timezone.

### Idempotency

Generation is idempotent:
- The `ON CONFLICT DO NOTHING` clause on the family-month uniqueness key prevents
  duplicate inserts.
- Re-running generation for the same org/month reports `existing` count for each
  obligation that already exists.
- The generation audit record uses `system/cli` identity for all actions.

### Dry Run Mode

The `--dry-run` flag (or `dryRun: true` in the API) previews what would be generated
without persisting any obligations. The result reports `created` (would-create),
`existing`, `conflicts`, and `skipped` counts.

---

## API Endpoints

### Requirements

| Method | Path | Permission | Description |
|--------|------|-----------|-------------|
| POST   | `/v1/organizations/{orgId}/requirements` | CanManageRequirements | Create a requirement family with its first version |
| GET    | `/v1/organizations/{orgId}/requirements` | CanViewRequirements | List requirement families |
| GET    | `/v1/organizations/{orgId}/requirements/{key}` | CanViewRequirements | List versions for a family |
| POST   | `/v1/organizations/{orgId}/requirements/{key}/versions` | CanManageRequirements | Publish a new version |

### Requirement Assignments

| Method | Path | Permission | Description |
|--------|------|-----------|-------------|
| POST   | `/v1/organizations/{orgId}/requirement-assignments` | CanManageAssignments | Create an assignment |
| GET    | `/v1/organizations/{orgId}/requirement-assignments` | CanViewAssignments | List assignments |
| GET    | `/v1/organizations/{orgId}/requirement-assignments/{id}` | CanViewAssignments | Get an assignment |
| POST   | `/v1/organizations/{orgId}/requirement-assignments/{id}/end` | CanManageAssignments | End an assignment |
| POST   | `/v1/organizations/{orgId}/requirement-assignments/{id}/transition` | CanManageAssignments | Transition to a new version |

### Obligations

| Method | Path | Permission | Description |
|--------|------|-----------|-------------|
| POST   | `/v1/organizations/{orgId}/obligations/generate` | CanGenerateObligations | Generate monthly obligations |
| GET    | `/v1/organizations/{orgId}/obligations` | CanViewObligations | List obligations |
| GET    | `/v1/organizations/{orgId}/obligations/{id}` | CanViewObligations | Get an obligation |

### Generate Request Body

```json
{
  "year": 2026,
  "month": 1,
  "dryRun": false
}
```

### Generate Response

```json
{
  "periodStart": "2026-01-01",
  "periodEnd": "2026-02-01",
  "created": 12,
  "existing": 0,
  "conflicts": 0,
  "skipped": 3
}
```

---

## CLI Tool

The `phase3` CLI provides a `generate-obligations` subcommand for scheduled or ad-hoc
generation:

```bash
DATABASE_URL="postgres://..." ./phase3 generate-obligations --org <uuid> --year 2026 --month 1
DATABASE_URL="postgres://..." ./phase3 generate-obligations --org <uuid> --year 2026 --month 1 --dry-run
```

The CLI uses the `system/cli` identity for audit records.

---

## Migration: 000004

### Up Migration
- Creates `requirements` table (family-level concept)
- Adds `requirement_key` column to `requirement_versions` (backfilled from existing UUIDs)
- Adds `reporting_timezone` column to `requirement_versions` (for obligation calculation)
- Creates `requirement_assignments` table with overlap exclusion constraint
- Adds columns to `obligations`: `requirement_key`, `coverage_start`, `coverage_end`, `state`
- Adds uniqueness constraint `obligations_family_month_key`
- Creates indexes for obligation filtering

### Down Migration
The down migration refuses if any Phase 3 data exists in `obligations`, `requirement_versions`,
`requirements`, or `requirement_assignments`. This prevents data loss during a downgrade.

---

## Integration with Phase 2

- **Engagements** connect contractors to sites. Requirement assignments reference
  engagements to scope obligations to the correct contractor-site relationship.
- **Sites** must be active for the engagement to be considered during obligation generation.
- **Contractors** receive obligation notifications; the obligation's derived status
  includes a `contractorOverdue` flag based on the due date.
- **Reviews/Submissions** (Phase 4) reference obligations via `submission.obligation_id`.
