# Phase 2 — Contractors, sites, and engagements

This document describes the implemented Phase 2 business module. It is deliberately
concrete about invariants, authorization, concurrency, and rollback so the behavior can
be reviewed against the code.

## 1. Domain and use cases

A tenant (organization) operates one or more **sites**. It registers external
**contractors** under its own directory. A dated **engagement** connects one contractor
to one site for that tenant.

The same real-world contractor can be registered independently by two tenants. There is
no shared global directory. A contractor may be engaged at several sites at once, and a
site may host several contractors.

Typical uses:

- Register sites and contractors.
- Assign a contractor to one site, several sites, or a returning engagement later.
- Close an engagement when work stops and keep the record for history.
- Prevent new work at a deactivated site or with a deactivated contractor.

## 2. Site entity and lifecycle

Fields: `id`, `organizationId`, `name`, `isActive`, `createdAt`, `updatedAt`.

- Names are trimmed, 1–200 Unicode characters, and reject control characters.
- Names are not globally or tenant-unique.
- New sites are active.
- `POST .../deactivate` requires owner or manager.
- `POST .../reactivate` restores eligibility for new engagements.

Deactivation is refused with `409` while any engagement for the site has no end date or
ends after today. The engagement can end today: `active_until` is exclusive. Historical
engagements are untouched. Repeating deactivate (or reactivate) in the current state is
an idempotent `200` and writes no additional audit event.

## 3. Contractor entity and lifecycle

Fields: `id`, `organizationId`, `name`, `contactEmail`, `isActive`, `createdAt`,
`updatedAt`.

- Name rules match sites.
- `contactEmail` is optional, at most 320 bytes, rejects whitespace/control characters,
  and requires one `@` plus a dotted domain. The domain is lowercased; the local part
  keeps its case because local-part case equivalence is not assumed.
- The email is contact data only. It is not an identity and is never used for
  authentication or membership lookup.
- Deactivation and reactivation behave exactly like sites, including the current/future
  engagement guard and idempotent repeats.

`PATCH` distinguishes an omitted property from an explicit null: omitted fields are
preserved, `{"contactEmail": null}` (or a blank string) clears the address. There is no
hard-delete route.

## 4. Engagement entity and lifecycle

Fields: `id`, `organizationId`, `contractorId`, `siteId`, `activeFrom`, `activeUntil`,
derived `state`, `createdAt`, `updatedAt`.

The interval is half-open `[active_from, active_until)`:

- `active_from` is inclusive.
- `active_until` is exclusive.
- `active_until = null` is open-ended.
- `active_until` must be strictly later than `active_from`.

States are derived against an evaluation date and never stored:

| State | Rule |
| --- | --- |
| `scheduled` | evaluation date is before `active_from` |
| `active` | evaluation date is on/after `active_from` and before `active_until` |
| `ended` | `active_until` is not null and evaluation date is on/after it |

For responses and list filters, the evaluation date is the server's UTC calendar date
unless the caller supplies `asOf=YYYY-MM-DD`. No organization timezone is assumed; none
exists until a later phase.

Operations:

- **Create** — validated dates, both parents in the tenant, both active, no overlap.
- **Retrieve/list** — tenant scoped, derived state.
- **Correct** (`PATCH`) — date-only, at least one field, guarded by downstream history.
- **Close** (`POST .../close`) — sets an end date later than the start.

Closing an open engagement to the same date twice is an idempotent `200`. Closing an
already closed engagement with a different date is `409`; `PATCH` is the explicit
correction path.

Engagements are never hard-deleted. Their ID survives corrections and closes because
Phase 3 assignments, obligations, submissions, and reviews will reference it. A returning
contractor gets a new, adjacent or later, engagement.

## 5. Overlap prevention

Both layers enforce the invariant:

1. **Database authority:** `engagements_no_overlap`, a GiST exclusion constraint on
   `(organization_id WITH =, contractor_id WITH =, site_id WITH =,
   daterange(active_from, active_until, '[)') WITH &&)`. `btree_gist` is installed by the
   migration. This also protects direct database writes and concurrent transactions.
2. **Application:** the service counts intersecting rows in the locked transaction to
   return a clean `409` before the insert/update.

The migration refuses to apply (with the count and an example pair) if pre-existing rows
already overlap. The down migration refuses on populated Phase 2 tables.

`btree_gist` ships with official PostgreSQL images and is available on managed services;
the migration fails loudly if the database role cannot install it. If a deployment
forbids extensions, the application check and organization lock remain, but direct
database writes would only be protected by the service layer.

## 6. Deactivation rules

A site or contractor is deactivation-blocked while `active_until IS NULL OR
active_until > today(UTC)`. Deactivation never edits engagements. Reactivation does not
reopen any old engagement.

Creation rejects an inactive parent with `409`, so deactivation and creation cannot both
commit an inconsistent state (they serialize on the organization row).

## 7. Correction guards

`PATCH` only accepts `activeFrom` and `activeUntil`; tenant, engagement ID, contractor,
and site are fixed.

Before writing, the service counts downstream references:

- With **obligations**, every `[period_start, period_end)` must remain inside the new
  interval, otherwise `409` (`historical references prevent this change`).
- With **assignments but no obligations**, only extensions are allowed (start not later,
  and end not earlier / not newly bounded), because assignments have no date window.
- With no references, any non-overlapping valid period is allowed, subject to the
  parent-activity check below.

Closing applies the same obligation-coverage guard.

Correction also re-checks parent activity under the organization lock. If either the
contractor or site is inactive and the proposed interval still covers today or a later
date (`activeUntil` null or `> today(UTC)`), the correction is refused with `409` and no
audit row. Purely historical corrections and shortenings ending today or earlier remain
allowed. This closes the gap where a past, closed engagement could be reopened into the
present after its parent had been deactivated. `Close` is unchanged: it only shortens.

## 8. Permission matrix

| Operation | Owner | Manager | Reviewer |
| --- | --- | --- | --- |
| List/view sites | yes | yes | yes |
| Create/update site | yes | yes | no |
| Deactivate/reactivate site | yes | yes | no |
| List/view contractors | yes | yes | yes |
| Create/update contractor | yes | yes | no |
| Deactivate/reactivate contractor | yes | yes | no |
| List/view engagements | yes | yes | yes |
| Create/correct/close engagement | yes | yes | no |

Membership administration remains owner-only. The permissions live in `authz`; they do
not change Phase 1 meaning.

## 9. Tenant isolation

- Every SQL statement filters by `organization_id`.
- Path IDs are always combined with the tenant; another tenant's UUID is
  indistinguishable from a missing one (`404`).
- Contractor and site references in an engagement are loaded under the engaging tenant.
- Missing membership is `404`; an active member without the role is `403`; a bad token is
  `401`.
- Composite foreign keys continue to enforce parent ownership in the database.

## 10. API

All bodies are JSON, unknown fields are rejected, and problems are RFC 9457 documents
with `X-Request-Id`.

| Method | Path | Minimum role | Success |
| --- | --- | --- | --- |
| POST | `/v1/organizations/{orgId}/sites` | manager | 201 |
| GET | `/v1/organizations/{orgId}/sites` | reviewer | 200 |
| GET | `/v1/organizations/{orgId}/sites/{siteId}` | reviewer | 200 |
| PATCH | `/v1/organizations/{orgId}/sites/{siteId}` | manager | 200 |
| POST | `/v1/organizations/{orgId}/sites/{siteId}/deactivate` | manager | 200 |
| POST | `/v1/organizations/{orgId}/sites/{siteId}/reactivate` | manager | 200 |
| POST | `/v1/organizations/{orgId}/contractors` | manager | 201 |
| GET | `/v1/organizations/{orgId}/contractors` | reviewer | 200 |
| GET | `/v1/organizations/{orgId}/contractors/{contractorId}` | reviewer | 200 |
| PATCH | `/v1/organizations/{orgId}/contractors/{contractorId}` | manager | 200 |
| POST | `/v1/organizations/{orgId}/contractors/{contractorId}/deactivate` | manager | 200 |
| POST | `/v1/organizations/{orgId}/contractors/{contractorId}/reactivate` | manager | 200 |
| POST | `/v1/organizations/{orgId}/engagements` | manager | 201 |
| GET | `/v1/organizations/{orgId}/engagements` | reviewer | 200 |
| GET | `/v1/organizations/{orgId}/engagements/{engagementId}` | reviewer | 200 |
| PATCH | `/v1/organizations/{orgId}/engagements/{engagementId}` | manager | 200 |
| POST | `/v1/organizations/{orgId}/engagements/{engagementId}/close` | manager | 200 |

Examples:

```json
POST /v1/organizations/{org}/sites   {"name": "Factory A"}
POST /v1/organizations/{org}/contractors
  {"name": "ABC Security Services", "contactEmail": "ops@example.com"}
POST /v1/organizations/{org}/engagements
  {"contractorId": "...", "siteId": "...", "activeFrom": "2026-11-01", "activeUntil": null}
PATCH /v1/organizations/{org}/engagements/{id}
  {"activeUntil": "2027-03-01"}
POST /v1/organizations/{org}/engagements/{id}/close
  {"activeUntil": "2027-03-01"}
```

`close` requires a body; `deactivate`/`reactivate` require none.

## 11. Filtering and pagination

Lists use keyset pagination: `limit` 1–200 (default 50), opaque `cursor`, response
`nextCursor`. Ordering is `(created_at, id)`, so identical timestamps cannot duplicate or
skip rows.

- Sites/contractors: `isActive=true|false`, `q` (bounded substring, `%`/`_` escaped).
- Engagements: `contractorId`, `siteId`, `state`, `asOf`.

All filters include the tenant predicate. Lists are backed by tenant-scoped indexes.

## 12. Migration 000003

Additive only:

- `sites.is_active` (default true), `sites.updated_at`; name length check; indexes.
- `contractors.updated_at`; name/email length checks; indexes.
- `engagements.updated_at`; pair/date indexes; exclusion constraint; `btree_gist`.

Compatibility is tested for: empty → latest, populated v2 → v3 with rows retained, and an
overlapping v2 dataset (fails safely with a diagnostic, version left dirty).

`000003.down` succeeds only while `sites`, `contractors`, `engagements`,
`requirement_assignments`, and `obligations` are all empty. Otherwise it raises a `P2`
error naming the populated tables; the ledger is then repaired with `migrate force 3`.
Dropping `is_active`/`updated_at` from rows would silently lose deactivation state and
modification history, so refusal is deliberate. The extension is kept on downgrade.

## 13. Audit events

`site.created|updated|deactivated|reactivated`,
`contractor.created|updated|deactivated|reactivated`,
`engagement.created|corrected|closed`.

Each event carries the actor, tenant, resource type/ID, and small details: changed field
names and previous/current dates or names. Contact email values are not logged. Audit
inserts share the business transaction; an audit failure rolls the mutation back (HTTP
tests rename the table to prove it).

## 14. Concurrency and transactions

Every write uses one total lock order:

1. `SELECT ... FOR UPDATE` on the organization row.
2. Authorize the active membership in the same transaction.
3. Read/mutate entity rows.

Because all Phase 2 writes and Phase 1 membership mutations take the organization lock
first, there are no nested entity locks and no deadlock cycle. Consequences:

- Two overlapping creates serialize; the loser sees the committed row and gets `409`.
- Deactivation and creation serialize; the final state is consistent.
- Two closes with different dates serialize; one wins, the other conflicts.
- A revocation that commits while a write waits is observed by the post-lock
  authorization; the revoked manager gets `404`.

Reads use a repeatable-read, read-only transaction so authorization and data come from
one snapshot. Direct SQL writes that bypass the service still get the exclusion
constraint, but not the application conflict message or lock discipline.

## 15. Local smoke test

See the Phase 2 demonstration in the PR report. In outline: start PostgreSQL, migrate,
run `cmd/devidp`, bootstrap an organization as owner, create sites/contractors through
the API, create non-conflicting and conflicting engagements, close one, deactivate and
reactivate, then repeat reads as a reviewer and from a second tenant.

## 16. Tests

```sh
gofmt -l .
go vet ./...
REQUIRE_DATABASE=1 go test ./...
REQUIRE_DATABASE=1 go test -race ./...
./scripts/verify-integration.sh
```

`verify-integration.sh` now requires the `sites`, `contractors`, `engagements`,
`orgaccess`, `database`, `organizations`, `requirements`, `assignments`,
`obligations`, and `httpserver` packages and fails on any skip.

## 17. Known limitations

- Deactivation is immediate; scheduled future deactivation is a separate feature.
- Name search uses `ILIKE` without trigram indexes; adequate at small/medium tenant size.
- Rate limiting, RLS, metrics, and TLS are unchanged from Phase 1.
- `asOf` accepts any calendar date; there is no organization timezone yet.
- A same-tenant member can distinguish `409` causes by problem detail, which is intended.
- Extension installation needs a role allowed to `CREATE EXTENSION btree_gist`.

## 18. Phase 3 integration

`requirement_assignments` and `obligations` already participate in correction guards.
Phase 3 can attach templates and generators without changing an engagement's identity.
When assignment/obligation semantics exist, the conservative assignment-extension guard
can be relaxed with a dedicated, audited policy.

### Integration points

- **Engagement active range**: Phase 3 obligation generation reads `engagements.active_from`
  and `engagements.active_until` to compute the coverage intersection with each reporting
  month. A closed or scheduled engagement reduces the obligation coverage window.
- **Site and contractor deactivation**: Deactivating a site or contractor sets
  `is_active=false`, but does not end engagements. Phase 3 reads the engagement's
  `active_until` (not the parent's active flag) when determining coverage. Closing the
  engagement is still the canonical way to end obligation generation for a path.
- **Engagement correction guard**: The `PATCH` and `close` endpoints count downstream
  `obligations` rows and refuse changes that would move `period_start`/`period_end`
  outside any obligation's existing coverage window. This prevents silently invalidating
  already-generated evidence requirements.
- **Migration 000003 down guard**: The down migration checks `obligations` and
  `requirement_assignments` tables (in addition to `sites`, `contractors`, and
  `engagements`) to prevent rollback when Phase 3 data exists.
- **Audit trail**: Phase 3 generation events record `system/cli` as the actor when run
  from the `cmd/phase3` CLI, preserving an auditable chain from obligation creation
  through Phase 4 submission/review.
