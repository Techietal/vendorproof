# VendorProof MVP — Acceptance criteria

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
