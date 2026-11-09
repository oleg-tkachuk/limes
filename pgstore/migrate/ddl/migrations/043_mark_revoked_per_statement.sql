-- A statement's revocation entries mark their records and every record
-- below them together, a level at a time, as 015's walk does for one entry.
-- 015 walked once per entry: each walk reads the children of its level, and
-- where the statistics say every record is revoked already, the planner
-- reads them through capabilities_tenant_unrevoked, every record unrevoked
-- of every tenant, for each entry, so a statement of n entries over n
-- records read n² rows. Walked once per statement, a level is read once
-- however it is planned, and the walk ends within MaxDelegationDepth levels.
--
-- Each level is still marked by one statement, which waits for any insert
-- of a child holding its row FOR SHARE (015), so a record and its parent's
-- revocation never pass each other.
DROP TRIGGER revocations_mark_revoked ON limes.revocations;

CREATE OR REPLACE FUNCTION limes.mark_revoked() RETURNS trigger
LANGUAGE plpgsql
-- Pinned again, as 015 pinned it: a replaced function keeps no setting.
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    level uuid[];
BEGIN
    SELECT array_agg(DISTINCT capability_id) INTO level FROM written;
    LOOP
        EXIT WHEN level IS NULL;
        WITH marked AS (
            UPDATE limes.capabilities SET revoked = true
            WHERE id = ANY (level) AND NOT revoked
            RETURNING id
        )
        SELECT array_agg(id) INTO level FROM marked;
        EXIT WHEN level IS NULL;
        SELECT array_agg(id) INTO level FROM limes.capabilities
        WHERE parent_id = ANY (level) AND NOT revoked;
    END LOOP;
    RETURN NULL;
END
$$;

CREATE TRIGGER revocations_mark_revoked AFTER INSERT ON limes.revocations
    REFERENCING NEW TABLE AS written FOR EACH STATEMENT EXECUTE FUNCTION limes.mark_revoked();
