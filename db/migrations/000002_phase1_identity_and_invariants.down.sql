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

-- The old constraint is per subject (not issuer) and only one row per subject
-- can survive. Keep an active row where possible, else the newest revoked one.
DELETE FROM memberships m
WHERE m.id NOT IN (
    SELECT DISTINCT ON (organization_id, identity_subject) id
    FROM memberships
    ORDER BY organization_id, identity_subject, (revoked_at IS NULL) DESC, created_at DESC, id
);

ALTER TABLE memberships
    ADD CONSTRAINT memberships_organization_id_identity_subject_key
        UNIQUE (organization_id, identity_subject),
    DROP CONSTRAINT memberships_subject_length,
    DROP CONSTRAINT memberships_issuer_length,
    DROP COLUMN revoked_at,
    DROP COLUMN updated_at,
    DROP COLUMN identity_issuer;
