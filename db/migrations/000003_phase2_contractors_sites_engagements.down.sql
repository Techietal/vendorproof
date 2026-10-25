-- 000003 introduced lifecycle columns (site activity, update timestamps), the
-- engagement overlap constraint, and indexes. Dropping the columns from rows
-- loses information even though no business row is deleted: a deactivated site
-- would silently become active again and audit trail of modification times
-- would disappear. Any Phase 2 row therefore refuses the downgrade.
--
-- The btree_gist extension is intentionally kept: other objects may use it and
-- dropping it can fail independently of this migration.
DO $$
DECLARE populated text;
BEGIN
    SELECT string_agg(t.name, ', ' ORDER BY t.name) INTO populated
    FROM (
        SELECT 'sites' AS name WHERE EXISTS (SELECT 1 FROM sites)
        UNION ALL SELECT 'contractors' WHERE EXISTS (SELECT 1 FROM contractors)
        UNION ALL SELECT 'engagements' WHERE EXISTS (SELECT 1 FROM engagements)
        UNION ALL SELECT 'requirement_assignments' WHERE EXISTS (SELECT 1 FROM requirement_assignments)
        UNION ALL SELECT 'obligations' WHERE EXISTS (SELECT 1 FROM obligations)
    ) t;

    IF populated IS NOT NULL THEN
        RAISE EXCEPTION 'cannot roll back 000003: Phase 2 data exists in %', populated
            USING HINT = 'Inspect the schema, verify it is intact, then repair the dirty ledger with migrate force 3.';
    END IF;
END;
$$;

DROP INDEX engagements_org_until;
DROP INDEX engagements_org_pair_from;
DROP INDEX engagements_org_site_created;
DROP INDEX engagements_org_contractor_created;
DROP INDEX engagements_org_created;
DROP INDEX contractors_org_name;
DROP INDEX contractors_org_active_created;
DROP INDEX sites_org_name;
DROP INDEX sites_org_active_created;

ALTER TABLE engagements DROP CONSTRAINT engagements_no_overlap;
ALTER TABLE engagements DROP COLUMN updated_at;

ALTER TABLE contractors
    DROP CONSTRAINT contractors_contact_email_length,
    DROP CONSTRAINT contractors_name_length,
    DROP COLUMN updated_at;

ALTER TABLE sites
    DROP CONSTRAINT sites_name_length,
    DROP COLUMN updated_at,
    DROP COLUMN is_active;
