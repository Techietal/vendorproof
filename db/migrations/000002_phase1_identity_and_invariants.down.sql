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

DROP INDEX memberships_org_created;
DROP INDEX memberships_identity_lookup;
DROP INDEX memberships_organization_id_id_unique;
DROP INDEX memberships_active_identity_unique;

-- The deprecated constraint is per subject, so rollback is lossless only when
-- each (organization_id, identity_subject) pair has exactly one row. Refuse
-- rather than silently deleting revoked/cross-issuer history. Because
-- golang-migrate runs this file in one transaction, the schema stays intact.
DO $$
DECLARE duplicate_groups integer;
BEGIN
    SELECT count(*) INTO duplicate_groups
    FROM (
        SELECT 1
        FROM memberships
        GROUP BY organization_id, identity_subject
        HAVING count(*) > 1
    ) duplicates;

    IF duplicate_groups > 0 THEN
        RAISE EXCEPTION
            'cannot roll back 000002 losslessly: % membership (organization, subject) group(s) have multiple rows',
            duplicate_groups
            USING HINT = 'Merge/delete duplicate memberships, then: migrate force 2 and migrate down.';
    END IF;
END;
$$;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_organization_id_identity_subject_key
        UNIQUE (organization_id, identity_subject),
    DROP CONSTRAINT memberships_subject_length,
    DROP CONSTRAINT memberships_issuer_length,
    DROP COLUMN revoked_at,
    DROP COLUMN updated_at,
    DROP COLUMN identity_issuer;
