# Phase 1 — API, operations, and review notes

This document describes the implemented Phase 1 foundation. It distinguishes verified
behavior from work that still depends on external choices.

## Authentication model

Every `/v1/*` route requires `Authorization: Bearer <access token>`. The verifier:

1. restricts algorithms to `OIDC_ALLOWED_ALGORITHMS`;
2. requires the configured JOSE `typ` (default `at+jwt`; `application/at+jwt` is also
   accepted);
3. resolves `kid` from the configured JWKS, fetching and caching with a 1-minute floor
   and 1-hour ceiling and single-flight refresh;
4. verifies the signature against RSA/EC/Ed25519 public keys with key-type checks;
5. requires `exp`, validates `nbf`/`iat` when present, with 30 seconds of leeway;
6. requires an exact issuer (one trailing slash tolerated) and the configured audience;
7. requires a non-blank, bounded subject without control characters.

The authenticated principal is `(issuer, subject)`. OIDC subjects are namespaced by
issuer in the schema; the same `sub` at two issuers is two identities. `email` is never
used for identity.

### Provider selection

No production provider has been chosen. Configure all three `OIDC_*` values explicitly;
discovery documents are not fetched. The token shape must be checked against a real
provider before launch:

- Keycloak access tokens use `typ: Bearer`; set `OIDC_REQUIRED_TOKEN_TYPE=Bearer`.
- Auth0 access tokens commonly use `typ: JWT`; confirm from a real token before setting
  `JWT`. (ID tokens also use `JWT`, so the audience must be the API identifier, not a
  client ID.)
- If a provider's access tokens omit `typ`, set `OIDC_REQUIRED_TOKEN_TYPE=any` and rely
  on issuer/audience/signature checks; document the decision.

`cmd/devidp` is development-only. Its private key is untracked (`.dev/`).

## Endpoints

All bodies are JSON. Errors are `application/problem+json` with `type`, `title`,
`status`, `detail`, `instance`, and `requestId`. Successful responses include request
IDs in `X-Request-Id`; problem responses repeat it.

| Method | Path | Role | Notes |
| --- | --- | --- | --- |
| GET | `/health/live` | none | No database dependency |
| GET | `/health/ready` | none | `SELECT`-free `Ping`; 503 when unavailable |
| GET | `/v1/me` | any member identity | Identity and active memberships |
| GET | `/v1/organizations` | any member identity | Only caller's active memberships |
| GET | `/v1/organizations/{id}` | any member | Cross-tenant -> 404 |
| PATCH | `/v1/organizations/{id}` | owner | `{"name":"..."}`, 1–200 chars |
| GET | `/v1/organizations/{id}/memberships` | owner | Active only, keyset paginated |
| POST | `/v1/organizations/{id}/memberships` | owner | `{"issuer","subject","role"}` |
| PATCH | `/v1/organizations/{id}/memberships/{membershipId}` | owner | `{"role"}` |
| DELETE | `/v1/organizations/{id}/memberships/{membershipId}` | owner | 204; soft revoke |

List endpoints accept `limit` (1–200, default 50) and opaque `cursor`; responses carry
`nextCursor` (`null` at the end). Unknown JSON fields are rejected.

Status meanings: `400` validation, `401` missing/invalid authentication, `403`
authenticated member with insufficient role, `404` absent or invisible (including
cross-tenant), `409` conflict (duplicate membership, repeated revocation, last owner),
`413` body too large, `503` dependency timeout.

Example:

```sh
TOKEN=$(go run ./cmd/devidp token --subject owner-1 --key-file .dev/idp-key.pem)
ORG=$(curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/v1/organizations \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["organizations"][0]["id"])')

curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"issuer":"http://127.0.0.1:9000","subject":"manager-1","role":"manager"}' \
  "localhost:8080/v1/organizations/$ORG/memberships"
```

## Roles

- **Owner**: view, update configuration, list/add/revoke memberships, change roles.
- **Manager**: view configuration; no membership administration in Phase 1.
- **Reviewer**: view only.

Manager/reviewer operational permissions arrive in later phases. Authorization is
server-side after resolving an active membership from PostgreSQL. The frontend is never
trusted.

## Tenant isolation

- Every membership and organization statement filters by `organization_id`.
- Organization visibility comes from an active membership join.
- A membership UUID is always looked up with its organization, so another tenant's UUID
  is indistinguishable from a missing one (`404`).
- Writes use transactions and organization row locks.
- OIDC JWTs never carry organization scope; the organization is explicit in the path.
- Existing composite tenant foreign keys are preserved for future domain tables.

Negative tests cover direct and indirect cross-tenant references at service and HTTP
boundaries.

## Last-owner invariant

Every membership mutation begins with `SELECT ... FOR UPDATE` on the organization row.
Owner demotions/removals then count active owners in the same transaction and fail with
`409` if the count is one. Concurrent tests prove one of two simultaneous removals
fails and exactly one owner remains. Bootstrap always creates an owner atomically.

## Audit events

Actions: `organization.bootstrapped`, `organization.created`, `organization.updated`,
`membership.created`, `membership.role_changed`, `membership.revoked`.

Actor is the verified `(issuer, subject)` (for bootstrap, the first owner plus
`source=admin_cli`). Rows include organization, resource, timestamp, and small JSON
details (role before/after, subject, changed fields). They never include JWTs, secrets,
or full payloads. Rows are written in the same transaction as the mutation and are
append-only: a trigger rejects `UPDATE` and `DELETE`.

The bootstrap creates an organization, so `organization.created` and
`organization.bootstrapped` overlap in meaning; the latter adds provenance.

## Migration strategy

- Engine: `golang-migrate` as a library with embedded files and
  `schema_migrations(version, dirty)`.
- `000001_initial` is byte-identical to the applied development baseline.
- `000002_phase1_identity_and_invariants` forwards:
  - `identity_issuer` on memberships/reviews and `actor_issuer` on audit;
  - active-membership partial unique index while retaining revoked rows;
  - membership `revoked_at`/`updated_at`, organization `updated_at` and name-length
    check, supporting indexes;
  - the audit append-only trigger.
- Legacy rows backfill `identity_issuer='legacy:unconfigured'` before the default is
  dropped.
- The down migration deduplicates revoked/active identities before restoring the old
  constraint.

Use `go run ./cmd/migrate up|down|version|force N|baseline`. Tests apply up/down/up on
disposable databases and baseline a manually applied `000001` schema.

## Verification performed

On the EC2 host, against PostgreSQL 17.11:

- `go test ./...`, `go test -race ./...`, `go vet ./...` passed.
- Migration up/down/up and manual baseline passed in disposable databases.
- Bootstrap CLI, live/ready, `/v1/me`, organization read/update, membership add,
  manager 403, revocation 204 -> repeat 409 -> revoked 404, last-owner 409, and audit
  persistence were exercised with real signed tokens.
- SIGTERM drained the server and logged clean shutdown; the connection pool closed.

## Known limitations

- Access tokens outlive membership revocation cryptographically; authorization dies
  immediately because membership state is per-request.
- No HTTP organization creation, invitations, email discovery, or signup.
- No JWT introspection, token revocation endpoint, or proof-of-possession.
- In-process JWKS cache; multiple replicas each fetch independently.
- No RLS. Application SQL plus constraints enforce isolation.
- No rate limiting, TLS, proxy trust, metrics, or tracing.
- Audit actors are strings, not foreign keys; identities can disappear from the
  provider.
- The audit trigger can be bypassed by a superuser and does not hash-chain rows.
- Pagination is forward-only and capped at 200.
- Bootstrap requires direct database/CLI access on the host.
- Provider requirements above remain unverified until a real provider is selected.

## Phase 2 prerequisites

Select the provider, confirm access-token shape, add tenant-owned contractor/site
modules, and add upload grants and storage. Keep Phase 1 invariants intact.
