-- Phase 1: isolate identity by issuer, make memberships revocable, and protect audit history.
-- 000001 has already been applied to development databases, so it is not rewritten here.

-- Identity is the pair (issuer, subject). OIDC `sub` is only unique within an issuer.
ALTER TABLE memberships
    ADD COLUMN identity_issuer text NOT NULL DEFAULT 'legacy:unconfigured',
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN revoked_at timestamptz;

ALTER TABLE memberships ALTER COLUMN identity_issuer DROP DEFAULT;

ALTER TABLE memberships
    ADD CONSTRAINT memberships_issuer_length CHECK (char_length(identity_issuer) BETWEEN 1 AND 2048),
    ADD CONSTRAINT memberships_subject_length CHECK (char_length(btrim(identity_subject)) BETWEEN 1 AND 255);

-- Only one *active* membership per identity and organization; revoked rows are retained.
ALTER TABLE memberships DROP CONSTRAINT memberships_organization_id_identity_subject_key;
CREATE UNIQUE INDEX memberships_active_identity_unique
    ON memberships (organization_id, identity_issuer, identity_subject)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX memberships_organization_id_id_unique ON memberships (organization_id, id);
CREATE INDEX memberships_identity_lookup
    ON memberships (identity_issuer, identity_subject)
    WHERE revoked_at IS NULL;
CREATE INDEX memberships_org_created ON memberships (organization_id, created_at, id);

ALTER TABLE organizations
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT organizations_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 200);
CREATE INDEX organizations_created ON organizations (created_at, id);

-- Reviewers and audit actors carry the same issuer-qualified identity.
ALTER TABLE reviews
    ADD COLUMN identity_issuer text NOT NULL DEFAULT 'legacy:unconfigured';
ALTER TABLE reviews ALTER COLUMN identity_issuer DROP DEFAULT;
ALTER TABLE reviews
    ADD CONSTRAINT reviews_issuer_length CHECK (char_length(identity_issuer) BETWEEN 1 AND 2048);

ALTER TABLE audit_events
    ADD COLUMN actor_issuer text NOT NULL DEFAULT 'legacy:unconfigured';
ALTER TABLE audit_events ALTER COLUMN actor_issuer DROP DEFAULT;
ALTER TABLE audit_events
    ADD CONSTRAINT audit_actor_issuer_length CHECK (char_length(actor_issuer) BETWEEN 1 AND 2048),
    ADD CONSTRAINT audit_actor_subject_length CHECK (char_length(actor_subject) BETWEEN 1 AND 255),
    ADD CONSTRAINT audit_action_length CHECK (char_length(action) BETWEEN 1 AND 100),
    ADD CONSTRAINT audit_resource_type_length CHECK (char_length(resource_type) BETWEEN 1 AND 100);
CREATE INDEX audit_events_by_org_actor ON audit_events (organization_id, actor_issuer, actor_subject);

-- Audit history is append-only at the database boundary, not just by convention.
CREATE FUNCTION audit_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only' USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER audit_events_no_update
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
