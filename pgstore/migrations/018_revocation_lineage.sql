-- limes.revocation_lineage answers as limes.is_revoked does, and names the
-- ancestors on record the walk read, nearest first: Store.RevocationLineage,
-- which a RevocationCache keeps beside each "not revoked" answer so that a
-- revocation drops only the answers it outdates.
CREATE FUNCTION limes.revocation_lineage(target uuid, max_depth integer)
RETURNS TABLE (self boolean, deepest integer, in_chain boolean, ancestors uuid[])
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
        coalesce(array_agg(chain.id ORDER BY chain.depth) FILTER (WHERE chain.depth > 0), '{}')
    FROM chain;
    PERFORM set_config('limes.all_tenants', caller_scope, true);
END
$$;

REVOKE EXECUTE ON FUNCTION limes.revocation_lineage(uuid, integer) FROM PUBLIC;

-- A statement that revokes capabilities notifies with the ids a cache must
-- drop the answers of (pgstore.RevocationWatcher), space-separated: each
-- capability it revoked whose parent has no revocation entry. A cached
-- answer about a descendant names its ancestors, so the topmost id of a
-- cascade reaches the rest of it, and a capability under a parent revoked
-- before has no "not revoked" answer to drop. Past 100 such ids
-- (pgstore.MaxNotifiedRevocations), and for a Biscuit copy's revocation,
-- the payload is empty, which clears a cache whole, as every payload did
-- before; a watcher of an older pgstore clears whole on any.
CREATE OR REPLACE FUNCTION limes.notify_revocation() RETURNS trigger
LANGUAGE plpgsql
-- Pinned again, as 007 pinned it: a replaced function keeps no setting.
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    named text[];
BEGIN
    IF NOT EXISTS (SELECT 1 FROM written) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME <> 'revocations' THEN
        PERFORM pg_notify('limes_revocations', '');
        RETURN NULL;
    END IF;
    SELECT array_agg(w.capability_id::text) INTO named
    FROM (
        SELECT w.capability_id FROM written w
        JOIN limes.capabilities c ON c.id = w.capability_id
        WHERE c.parent_id IS NULL
           OR NOT EXISTS (SELECT 1 FROM limes.revocations p WHERE p.capability_id = c.parent_id)
        LIMIT 101
    ) w;
    IF named IS NULL THEN
        RETURN NULL;
    END IF;
    IF cardinality(named) > 100 THEN
        PERFORM pg_notify('limes_revocations', '');
    ELSE
        PERFORM pg_notify('limes_revocations', array_to_string(named, ' '));
    END IF;
    RETURN NULL;
END
$$;
