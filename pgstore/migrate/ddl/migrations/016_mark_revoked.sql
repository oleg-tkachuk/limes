-- Marks the records revoked before 015's triggers: every record with a
-- revocation entry and every record below one, a level at a time as
-- limes.mark_revoked marks them, so a child inserted meanwhile under a
-- record not marked yet is either read by the next level or waits on its
-- parent's row and inherits the mark. A revocation written since 015 has
-- marked its records already, which the walk passes over.
DO $$
DECLARE
    level uuid[];
BEGIN
    SELECT array_agg(capability_id) INTO level FROM limes.revocations;
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
END
$$;
