# VendorProof — backend (development codename)

**Status: Phase 2 backend module.** Phase 1 authentication, tenancy, memberships, audit,
and migrations remain in place. Phase 2 adds sites, contractors, and durable dated
engagements with database-enforced overlap protection, lifecycle guards, and reviewer
read-only access. No document workflow, frontend, or deployment automation exists yet;
the name VendorProof requires rebranding before launch.

See [docs/03-phase-1-foundation.md](docs/03-phase-1-foundation.md) for the foundation,
[docs/04-phase-2-contractor-sites.md](docs/04-phase-2-contractor-sites.md) for the
business module, and [docs/01-mvp-acceptance.md](docs/01-mvp-acceptance.md) /
[docs/02-system-decisions.md](docs/02-system-decisions.md) for domain background.

## Architecture

A modular monolith in Go with explicit boundaries:

| Path | Responsibility |
| --- | --- |
| `cmd/api` | Process composition, timeouts, graceful shutdown |
| `cmd/admin` | Local `bootstrap-org` command (first owner) |
| `cmd/migrate` | Versioned migration runner; never runs inside the API |
| `cmd/devidp` | Development-only RS256 issuer and JWKS endpoint |
| `internal/httpserver` | Router, middleware, thin HTTP handlers |
| `internal/organizations` | Organizations, memberships, transactions, invariants |
| `internal/sites` | Site directory and lifecycle |
| `internal/contractors` | Contractor directory and lifecycle |
| `internal/engagements` | Dated assignments, overlap and correction guards |
| `internal/orgaccess` | Shared tenant locking and membership authorization |
| `internal/apperr` | Transport-neutral business error classes |
| `internal/validate` | Name, email, and calendar-date validation |
| `internal/pagination` | Shared keyset cursors |
| `internal/auth` | JWT verification and cached JWKS with rotation |
| `internal/authz` | Pure organization-role decisions |
| `internal/audit` | Append-only audit inserts inside business transactions |
| `internal/database` | pgx pool, transaction helper, embedded migration engine |
| `internal/config` | Environment validation with loopback-by-default binding |
| `internal/httpx` | RFC 9457 problems and strict JSON decoding |
| `internal/obligations` | Pre-existing pure monthly-obligation logic (unchanged) |
| `db/migrations` | `000001`–`000002` (unchanged) and forward-only Phase 2 `000003` |

PostgreSQL is the only datastore. Business mutations and their audit event share one
transaction. Tenant queries always carry `organization_id`; cross-tenant resources
return `404`.

## Requirements

- Go 1.25+ (toolchain used: Go 1.27)
- Docker with Compose (for local PostgreSQL 17)
- `sudo` for Docker on the provided EC2 host

## Configuration

Copy `.env.example` to `.env` (gitignored) and export it, or supply variables directly.
The API binds to `127.0.0.1` by default and is not internet-exposed.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `DATABASE_URL` | yes | — | PostgreSQL URL |
| `OIDC_ISSUER` | yes | — | Exact trusted issuer |
| `OIDC_AUDIENCE` | yes | — | Required `aud` value (the API's identifier) |
| `OIDC_JWKS_URL` | yes | — | Explicit JWKS endpoint |
| `PORT` / `HTTP_ADDR` | no | `127.0.0.1:8080` | Listener |
| `OIDC_ALLOWED_ALGORITHMS` | no | `RS256,ES256` | Signature algorithm allowlist |
| `OIDC_REQUIRED_TOKEN_TYPE` | no | `at+jwt` | Accepted JOSE `typ`; `any` disables |
| `OIDC_MIN_JWKS_REFRESH` / `OIDC_MAX_JWKS_REFRESH` | no | `1m` / `1h` | Refresh-rate clamp |
| `OIDC_STALE_KEY_GRACE` | no | `15m` | Maximum use of cached keys after expiry while refresh fails |
| `DB_MAX_CONNS` / `DB_MIN_CONNS` | no | `10` / `2` | Pool bounds |
| `REQUEST_TIMEOUT` / `SHUTDOWN_TIMEOUT` | no | `15s` / `15s` | Request and drain bounds |
| `MAX_BODY_BYTES` | no | `65536` | Body limit (413 beyond) |
| `LOG_LEVEL` | no | `info` | Structured JSON log level |

Issuer and JWKS URLs must use HTTPS unless the host is loopback (`localhost`,
`127.0.0.1`, `::1`), where plain HTTP is accepted for the development issuer.

## Start PostgreSQL and apply migrations

```sh
sudo docker compose up -d db
export DATABASE_URL='postgres://vendorproof:local-dev-password@localhost:5432/vendorproof?sslmode=disable'
go run ./cmd/migrate version
go run ./cmd/migrate up
```

`000001_initial` was applied manually to the existing development database before the
migration ledger existed. Such a database is baselined once, after confirming its
schema matches `000001`:

```sh
go run ./cmd/migrate baseline --confirm   # records version 1 only
go run ./cmd/migrate up         # applies forward migrations
```

A fresh database runs `up`; the one-time `baseline` path is only for the historical
database whose `000001` was applied manually. Baseline requires `--confirm` and a
complete catalog match against `000001` (tables, columns, types, nullability, defaults,
constraints, indexes, triggers, functions). Fresh or already-versioned databases are
refused.

`000003` installs `btree_gist` and an exclusion constraint, so the migrating role needs
permission to create that extension. `000003.down` is allowed only while sites,
contractors, engagements, assignment, and obligation tables are all empty.

`000002.down` is allowed only on an **empty** database. `000002` added issuer-qualified
identity, revocation state, and audit provenance; dropping those columns from populated
rows is semantic loss even when no row is deleted. Any row in any business table
refuses the downgrade. A refused downgrade leaves schema and data untouched but marks
the ledger dirty: inspect the v2 schema, then `migrate force 2`. For populated
databases, prefer staying on v2 or a forward fix over downgrading.

Never edit an applied migration; add a new one.

## Development identity provider and token

`cmd/devidp` signs real RS256 access tokens and serves a real JWKS. There is no auth
bypass; signatures are verified over HTTP.

```sh
go run ./cmd/devidp serve --addr 127.0.0.1:9000 --key-file .dev/idp-key.pem
# second shell
export OIDC_ISSUER=http://127.0.0.1:9000
export OIDC_AUDIENCE=vendorproof-api
export OIDC_JWKS_URL=http://127.0.0.1:9000/.well-known/jwks.json
TOKEN=$(go run ./cmd/devidp token --subject owner-1 --key-file .dev/idp-key.pem)
```

Use `--typ JWT` to mint an ID-token-style token (rejected by default). The key is
created under `.dev/` (gitignored).

## Bootstrap the first organization

There is deliberately no unauthenticated creation route. The CLI requires database
access (run it over SSH on EC2):

```sh
go run ./cmd/admin bootstrap-org \
  --name "Acme Roofing" \
  --issuer http://127.0.0.1:9000 \
  --subject owner-1
```

Organization creation, owner membership, and a `organization.bootstrapped` audit event
commit atomically. The issuer/subject must be the verified identity from the selected
provider, not an email guess.

## Run the API

```sh
set -a; . ./.env; set +a
go run ./cmd/api
curl http://127.0.0.1:8080/health/live
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/v1/me
```

Health, identity, and organization endpoints are described in
[docs/03-phase-1-foundation.md](docs/03-phase-1-foundation.md). Phase 2 site, contractor,
and engagement endpoints, roles, filters, and examples are in
[docs/04-phase-2-contractor-sites.md](docs/04-phase-2-contractor-sites.md):

```sh
ORG=$(curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/v1/organizations \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["organizations"][0]["id"])')
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Factory A"}' "localhost:8080/v1/organizations/$ORG/sites"
```

## Tests

Unit and disposable-database integration tests run together:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Integration tests create and drop their own `vp_test_*` databases. They use
`TEST_DATABASE_URL` when set (default:
`postgres://vendorproof:local-dev-password@localhost:5432/vendorproof?sslmode=disable`,
where the user must be able to create databases). With no reachable PostgreSQL they
skip and say so; `-short` skips them intentionally.

Acceptance runs must not skip silently. Set `REQUIRE_DATABASE=1` to fail instead of
skip, and use the proof script, which also fails on any skip:

```sh
REQUIRE_DATABASE=1 go test ./...
./scripts/verify-integration.sh
```

## Security guarantees implemented

- Identity comes only from a verified token (`iss` + `sub`); emails are not identity.
- Exact issuer/audience, signature, algorithm, `typ`, `exp`, `nbf`, and subject checks.
- JWKS caching is bounded, single-flighted, backoff-throttled during outages, and
  stale keys expire after a fixed grace window.
- Active membership is loaded from PostgreSQL on every protected request.
- Non-members get `404`; members lacking a role get `403`.
- Owner-only membership mutations; last-active-owner enforced under a row lock.
- Every Phase 2 write locks the organization row, re-authorizes, then mutates.
- Engagement overlaps are rejected by an exclusion constraint as well as the service.
- Reviewers are read-only for sites, contractors, and engagements.
- Revocation takes effect on the next request despite a still-valid JWT.
- Parameterized SQL with `organization_id` in every tenant query.
- Problems never include stack traces; tokens are never logged.
- Audit rows are inserted in the business transaction and are append-only by trigger.
- Request IDs, strict body limits, server timeouts, and graceful pool shutdown.
- Loopback binding and loopback-only PostgreSQL in Docker.

## Known limitations / Phase 2

- No production identity provider selected; the dev issuer is not production.
- Downgrades of populated databases are refused rather than transformed.
- Remote OIDC endpoints must be HTTPS; loopback HTTP is development-only.
- No email lookup, invitation delivery, or self-service signup — owner supplies
  issuer/subject.
- No refresh-token revocation; access tokens remain cryptographically valid until
  expiry, though revoked memberships lose access immediately.
- No organization creation HTTP endpoint (bootstrap CLI only).
- No rate limiting, TLS termination, metrics/tracing, or Row-Level Security.
- JWKS cache is per-process.
- Pagination is keyset-based, capped at 200.
- Deactivation is immediate; no scheduled future deactivation.
- Date-sensitive defaults use a server-side UTC date; no organization timezone yet.
- Requirements, uploads, reviews, reminders, billing, and frontend are out of scope.
  Phase 3 downstream tables already constrain engagement corrections.

## Domain notes (pre-existing)

- Reporting periods are half-open `[period_start, period_end)`.
- A monthly `due_day` falls in the following month, clamped; 17:00 local is a product
  default, not a statutory deadline.
- Rule versions are snapshotted; internal review delay never marks a contractor
  overdue.
- An engagement connects one contractor to one site; obligations are unique per
  tenant/engagement/version/period.
