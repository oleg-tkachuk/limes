-- limes.is_revoked answers from limes.revocation_lineage (018), which walks
-- the same chain and names the ancestors besides: one walk, written once,
-- so IsRevoked and RevocationLineage never answer apart. The signature and
-- the grants stay; a replaced function keeps no setting, so its search path
-- is pinned again.
CREATE OR REPLACE FUNCTION limes.is_revoked(target uuid, max_depth integer)
RETURNS TABLE (self boolean, deepest integer, in_chain boolean)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
    SELECT l.self, l.deepest, l.in_chain FROM limes.revocation_lineage(target, max_depth) l
$$;
