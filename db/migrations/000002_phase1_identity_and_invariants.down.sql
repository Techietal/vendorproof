-- 000002 superseded the v1 schema with issuer-qualified identity, revocation
-- timestamps, and audit actor issuers. Dropping those columns from populated
-- records is semantic data loss even when no rows are deleted, so populate any
-- row in any business table blocks the rollback. No records are transformed.
DO $$
DECLARE populated text;
BEGIN
    SELECT string_agg(t.name, ', ' ORDER BY t.name) INTO populated
    FROM (
        SELECT 'organizations' AS name WHERE EXISTS (SELECT 1 FROM organizations)
        UNION ALL SELECT 'memberships' WHERE EXISTS (SELECT 1 FROM memberships)
        UNION ALL SELECT 'sites' WHERE EXISTS (SELECT 1 FROM sites)
        UNION ALL SELECT 'contractors' WHERE EXISTS (SELECT 1 FROM contractors)
        UNION ALL SELECT 'engagements' WHERE EXISTS (SELECT 1 FROM engagements)
        UNION ALL SELECT 'requirement_versions' WHERE EXISTS (SELECT 1 FROM requirement_versions)
        UNION ALL SELECT 'requirement_assignments' WHERE EXISTS (SELECT 1 FROM requirement_assignments)
        UNION ALL SELECT 'obligations' WHERE EXISTS (SELECT 1 FROM obligations)
        UNION ALL SELECT 'documents' WHERE EXISTS (SELECT 1 FROM documents)
        UNION ALL SELECT 'submissions' WHERE EXISTS (SELECT 1 FROM submissions)
        UNION ALL SELECT 'reviews' WHERE EXISTS (SELECT 1 FROM reviews)
        UNION ALL SELECT 'notification_deliveries' WHERE EXISTS (SELECT 1 FROM notification_deliveries)
        UNION ALL SELECT 'audit_events' WHERE EXISTS (SELECT 1 FROM audit_events)
    ) t;

    IF populated IS NOT NULL THEN
        RAISE EXCEPTION 'cannot roll back 000002: business data exists in %', populated
            USING HINT = 'Inspect the schema, verify it is intact, then repair the dirty ledger with migrate force 2.';
    END IF;
END;
$$;

DROP INDEX memberships_org_created;
DROP INDEX memberships_identity_lookup;
DROP INDEX memberships_organization_id_id_unique;
DROP INDEX memberships_active_identity_unique;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_organization_id_identity_subject_key
        UNIQUE (organization_id, identity_subject),
    DROP CONSTRAINT memberships_subject_length,
    DROP CONSTRAINT memberships_issuer_length,
    DROP COLUMN revoked_at,
    DROP COLUMN updated_at,
    DROP COLUMN identity_issuer;

DROP TRIGGER audit_events_no_update ON audit_events;
DROP FUNCTION audit_events_append_only();

DROP INDEX audit_events_by_org_actor;
ALTER TABLE audit_events
    DROP CONSTRAINT audit_resource_type_length,
    DROP CONSTRAINT audit_action_length,
    DROP CONSTRAINT audit_actor_subject_length,
    DROP CONSTRAINT audit_actor_issuer_length,
    DROP COLUMN actor_issuer;

ALTER TABLE reviews
    DROP CONSTRAINT reviews_issuer_length,
    DROP COLUMN identity_issuer;

DROP INDEX organizations_created;
ALTER TABLE organizations
    DROP CONSTRAINT organizations_name_length,
    DROP COLUMN updated_at;
