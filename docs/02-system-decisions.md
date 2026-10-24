# Architectural decisions (initial)

1. **Modular monolith:** one Go service deployment with explicit domain boundaries. Worker may run as a separate process from the same codebase.
2. **Single transactional database:** PostgreSQL for tenant-owned relational records, job/outbox state, and review history.
3. **Requirement versions are immutable:** edits create new versions. Do not retroactively change generated obligations.
4. **Monthly periods:** half-open local calendar dates; store timestamp instants as UTC. Timezone is organization-configurable in the eventual API (initial pure-function code accepts a timezone).
5. **Lateness is derived:** outstanding contractor submission after due is late; internal review delay is not contractor late.
6. **Document storage:** private object store; metadata in PostgreSQL. Upload first lands in quarantine, passes scan and validation, then becomes reviewable.
7. **Idempotency:** obligation database unique constraints; notification unique delivery keys; transactional outbox for external sends.
8. **Security:** OIDC access-token verification (issuer, audience, signature, algorithm, type), identity as `(issuer, subject)`, organization-scoped RBAC resolved per request from PostgreSQL, and 404 for cross-tenant probes. Short-lived scoped upload grants and private buckets follow. Postgres composite foreign keys reject cross-organization references.
9. **Auditability:** append-only audit rows written in the business transaction; database trigger rejects update/delete; versioned submissions and human review decisions follow.
10. **Membership lifecycle:** soft revocation keeps history and allows re-invitation; a partial unique index permits one active row per identity. Owner mutations lock the organization row so the last active owner cannot disappear.
11. **Bootstrap:** the first owner is created by a local CLI against PostgreSQL, never an unauthenticated HTTP route.
12. **Scope:** evidence collection and review, not legal-compliance certification. Regulatory requirement templates are administrator-defined.

## Unresolved before launch

Select production identity provider and email service, finalize vendor invitation login model,
implement an enforceable retention policy, test migrations against PostgreSQL, confirm
operational security controls and any jurisdiction-specific data handling obligations.
