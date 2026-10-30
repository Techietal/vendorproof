-- 000004 is additive: it adds a requirements table, new columns on
-- requirement_versions/assignments/obligations, and new constraints.
--
-- Downgrade is allowed only on an empty database, because:
--   * requirement_versions.reporting_timezone affects existing due dates;
--     removing it would lose information about how historical obligations
--     were calculated.
--   * requirement_assignments.effective_from/effective_until are business
--     state; dropping them loses scheduling history.
--   * obligations.coverage_start/coverage_end, requirement_key, and state
--     are immutable facts about generated obligations; dropping them is
--     semantic loss.

DO $$
DECLARE
    populated text;
BEGIN
    SELECT string_agg(t.name, ', ' ORDER BY t.name) INTO populated
    FROM (
        SELECT 'requirements' AS name WHERE EXISTS (SELECT 1 FROM requirements)
        UNION ALL SELECT 'requirement_versions' WHERE EXISTS (SELECT 1 FROM requirement_versions)
        UNION ALL SELECT 'requirement_assignments' WHERE EXISTS (SELECT 1 FROM requirement_assignments)
        UNION ALL SELECT 'obligations' WHERE EXISTS (SELECT 1 FROM obligations)
    ) t;

    IF populated IS NOT NULL THEN
        RAISE EXCEPTION 'cannot roll back 000004: Phase 3 data exists in %', populated
            USING HINT = 'Inspect the schema, verify it is intact, then repair the dirty ledger with migrate force 4.';
    END IF;
END;
$$;

-- Drop constraints referencing the requirements table first.
ALTER TABLE requirement_versions
    DROP CONSTRAINT IF EXISTS requirement_versions_key_fkey;

ALTER TABLE requirement_assignments
    DROP CONSTRAINT IF EXISTS requirement_assignments_key_fkey;

ALTER TABLE obligations
    DROP CONSTRAINT IF EXISTS obligations_key_fkey;

-- Drop indexes.
DROP INDEX IF EXISTS obligations_org_contractor;
DROP INDEX IF EXISTS obligations_org_family;
DROP INDEX IF EXISTS obligations_org_due;
DROP INDEX IF EXISTS obligations_org_state;
DROP INDEX IF EXISTS requirement_assignments_org_family;
DROP INDEX IF EXISTS requirement_assignments_org_engagement;

-- Drop other constraints.
ALTER TABLE obligations
    DROP CONSTRAINT IF EXISTS obligations_coverage_order,
    DROP CONSTRAINT IF EXISTS obligations_family_month_key,
    DROP CONSTRAINT IF EXISTS obligations_engagement_version_period_key;

ALTER TABLE requirement_assignments
    DROP CONSTRAINT IF EXISTS requirement_assignments_no_overlap,
    DROP CONSTRAINT IF EXISTS requirement_assignments_version_key;

ALTER TABLE requirement_versions
    DROP CONSTRAINT IF EXISTS requirement_versions_timezone_check;

-- Drop the new columns.
ALTER TABLE obligations
    DROP COLUMN IF EXISTS state,
    DROP COLUMN IF EXISTS coverage_end,
    DROP COLUMN IF EXISTS coverage_start,
    DROP COLUMN IF EXISTS requirement_key;

ALTER TABLE requirement_assignments
    DROP COLUMN IF EXISTS requirement_key,
    DROP COLUMN IF EXISTS effective_until,
    DROP COLUMN IF EXISTS effective_from;

ALTER TABLE requirement_versions
    DROP COLUMN IF EXISTS reporting_timezone;

-- Drop the requirements table.
DROP TABLE IF EXISTS requirements;
