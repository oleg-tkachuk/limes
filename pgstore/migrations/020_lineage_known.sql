-- limes.revocation_lineage says whether the capability is on record at all
-- (known): one that is not is answered not revoked, as is_revoked answers
-- it, but Store.RevocationLineage reports it as not found, so a
-- RevocationCache keeps no answer for it, which no ancestor's revocation
-- could reach. A function's columns change only by dropping it; is_revoked
-- (019) reads it by name at each call and names its columns, so it reads
-- the new one as it read the old.
DROP FUNCTION limes.revocation_lineage(uuid, integer);

CREATE FUNCTION limes.revocation_lineage(target uuid, max_depth integer)
RETURNS TABLE (self boolean, deepest integer, in_chain boolean, ancestors uuid[], known boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    caller_scope text := coalesce(current_setting('limes.all_tenants', true), '');
BEGIN
    PERFORM set_config('limes.all_tenants', 'read', true);
    RETURN QUERY
    WITH RECURSIVE chain (id, parent_id, depth) AS (
        SELECT c.id, c.parent_id, 0 FROM limes.capabilities c WHERE c.id = target
      UNION ALL
        SELECT c.id, c.parent_id, chain.depth + 1
        FROM limes.capabilities c
        JOIN chain ON c.id = chain.parent_id
        WHERE chain.depth <= max_depth
    )
    SELECT
        EXISTS (SELECT 1 FROM limes.revocations r WHERE r.capability_id = target),
        coalesce(max(chain.depth), 0),
        EXISTS (SELECT 1 FROM chain JOIN limes.revocations r ON r.capability_id = chain.id),
        coalesce(array_agg(chain.id ORDER BY chain.depth) FILTER (WHERE chain.depth > 0), '{}'),
        count(chain.id) > 0
    FROM chain;
    PERFORM set_config('limes.all_tenants', caller_scope, true);
END
$$;

REVOKE EXECUTE ON FUNCTION limes.revocation_lineage(uuid, integer) FROM PUBLIC;
