-- Phase 2: site/contractor lifecycle and durable contractor-site engagements.
-- Existing columns and tables are preserved; every change is additive.
--
-- btree_gist is a PostgreSQL contrib extension (shipped with official images and
-- available on managed PostgreSQL). It is needed to combine equality on the
-- tenant/contractor/site UUIDs with an overlap test on the date range.

CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Pre-existing rows may have been written by hand before these APIs existed.
-- Detect values that would make the new constraints fail, and fail with a
-- diagnostic that names the offending rows instead of a bare catalog error.

DO $$
DECLARE
    bad_names integer;
    bad_emails integer;
    overlapping integer;
    example text;
BEGIN
    SELECT count(*) INTO bad_names FROM sites
        WHERE char_length(btrim(name)) NOT BETWEEN 1 AND 200;
    IF bad_names > 0 THEN
        RAISE EXCEPTION 'cannot apply 000003: % site(s) have a blank or overlength name', bad_names
            USING HINT = 'Correct site names to 1-200 trimmed characters, then retry.';
    END IF;

    SELECT count(*) INTO bad_names FROM contractors
        WHERE char_length(btrim(name)) NOT BETWEEN 1 AND 200;
    IF bad_names > 0 THEN
        RAISE EXCEPTION 'cannot apply 000003: % contractor(s) have a blank or overlength name', bad_names
            USING HINT = 'Correct contractor names to 1-200 trimmed characters, then retry.';
    END IF;

    SELECT count(*) INTO bad_emails FROM contractors
        WHERE contact_email IS NOT NULL AND char_length(contact_email) > 320;
    IF bad_emails > 0 THEN
        RAISE EXCEPTION 'cannot apply 000003: % contractor(s) have an overlength contact email', bad_emails
            USING HINT = 'Shorten contact_email to at most 320 characters, then retry.';
    END IF;

    SELECT count(*), min(a.id::text || ' / ' || b.id::text) INTO overlapping, example
    FROM engagements a
    JOIN engagements b
      ON a.organization_id = b.organization_id
     AND a.contractor_id = b.contractor_id
     AND a.site_id = b.site_id
     AND a.id < b.id
     AND daterange(a.active_from, a.active_until, '[)') && daterange(b.active_from, b.active_until, '[)');
    IF overlapping > 0 THEN
        RAISE EXCEPTION 'cannot apply 000003: % overlapping engagement pair(s) exist (example: %)', overlapping, example
            USING HINT = 'Close or correct the overlapping engagements without deleting history, then retry.';
    END IF;
END;
$$;

ALTER TABLE sites
    ADD COLUMN is_active boolean NOT NULL DEFAULT true,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT sites_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 200);

ALTER TABLE contractors
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT contractors_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    ADD CONSTRAINT contractors_contact_email_length CHECK (contact_email IS NULL OR char_length(contact_email) <= 320);

ALTER TABLE engagements
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

-- Half-open [active_from, active_until) ranges; NULL active_until is unbounded.
-- This is the authoritative overlap guard for direct database writes too.
ALTER TABLE engagements
    ADD CONSTRAINT engagements_no_overlap EXCLUDE USING gist (
        organization_id WITH =,
        contractor_id WITH =,
        site_id WITH =,
        daterange(active_from, active_until, '[)') WITH &&
    );

CREATE INDEX sites_org_active_created ON sites (organization_id, is_active, created_at, id);
CREATE INDEX sites_org_name ON sites (organization_id, name);
CREATE INDEX contractors_org_active_created ON contractors (organization_id, is_active, created_at, id);
CREATE INDEX contractors_org_name ON contractors (organization_id, name);
CREATE INDEX engagements_org_created ON engagements (organization_id, created_at, id);
CREATE INDEX engagements_org_contractor_created ON engagements (organization_id, contractor_id, created_at, id);
CREATE INDEX engagements_org_site_created ON engagements (organization_id, site_id, created_at, id);
CREATE INDEX engagements_org_pair_from ON engagements (organization_id, contractor_id, site_id, active_from);
CREATE INDEX engagements_org_until ON engagements (organization_id, active_until);
