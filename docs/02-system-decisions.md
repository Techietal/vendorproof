# Architectural decisions (initial)

1. **Modular monolith:** one Go service deployment with explicit domain boundaries. Worker may run as a separate process from the same codebase.
2. **Single transactional database:** PostgreSQL for tenant-owned relational records, job/outbox state, and review history.
3. **Requirement versions are immutable:** edits create new versions. Do not retroactively change generated obligations.
4. **Monthly periods:** half-open local calendar dates; store timestamp instants as UTC. Timezone is organization-configurable in the eventual API (initial pure-function code accepts a timezone).
5. **Lateness is derived:** outstanding contractor submission after due is late; internal review delay is not contractor late.
6. **Document storage:** private object store; metadata in PostgreSQL. Upload first lands in quarantine, passes scan and validation, then becomes reviewable.
7. **Idempotency:** obligation database unique constraints; notification unique delivery keys; transactional outbox for external sends.
8. **Security:** OIDC authentication and organization-scoped RBAC; short-lived scoped upload grants; deny public bucket access. Postgres composite foreign keys help reject cross-organization references.
9. **Auditability:** append-only actions, versioned submissions, human review decisions with reviewer ID/timestamp/reason.
10. **Scope:** evidence collection and review, not legal-compliance certification. Regulatory requirement templates are administrator-defined.

## Unresolved before launch

Select production identity provider and email service, finalize vendor invitation login model,
implement an enforceable retention policy, test migrations against PostgreSQL, confirm
operational security controls and any jurisdiction-specific data handling obligations.
