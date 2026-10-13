-- The revocation checks as functions that run with the owner's rights, so a
-- check is one statement on one round trip, outside any transaction the
-- store opens. The verifier asks before it knows the token's tenant, so a
-- check reads every tenant whatever the caller's scope: the owner is not
-- bound by the row-level security of 003_row_security.sql. They answer only
-- whether an id is revoked.

-- is_revoked walks the chain from target up, to one ancestor past
-- max_depth, which is how a chain too deep shows: whether the capability has
-- an entry of its own, how deep the walk went, and whether any capability on
-- it has an entry.
CREATE FUNCTION limes.is_revoked(target uuid, max_depth integer)
RETURNS TABLE (self boolean, deepest integer, in_chain boolean)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    WITH RECURSIVE chain (id, parent_id, depth) AS (
        SELECT id, parent_id, 0 FROM limes.capabilities WHERE id = target
      UNION ALL
        SELECT c.id, c.parent_id, chain.depth + 1
        FROM limes.capabilities c
        JOIN chain ON c.id = chain.parent_id
        WHERE chain.depth <= max_depth
    )
    SELECT
        EXISTS (SELECT 1 FROM limes.revocations WHERE capability_id = target),
        coalesce(max(depth), 0),
        EXISTS (SELECT 1 FROM chain JOIN limes.revocations r ON r.capability_id = chain.id)
    FROM chain
$$;

-- is_biscuit_revoked reports whether any of revocation_ids is revoked.
CREATE FUNCTION limes.is_biscuit_revoked(revocation_ids bytea[])
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT EXISTS (SELECT 1 FROM limes.biscuit_revocations WHERE revocation_id = ANY (revocation_ids))
$$;
