-- Row-level security binds the owner too. Until now the owner — the role
-- that migrates — read and wrote every tenant's rows, so a service that
-- connected as it had no isolation at all. With FORCE a role is unbound only
-- as a superuser or with BYPASSRLS, and CheckSchema refuses a store serving
-- as either, or as the owner.
--
-- The revocation checks run with the owner's rights to read every tenant
-- (004_revocation_checks.sql). Bound now, each scopes itself to every
-- tenant, read only, for its own query, and puts the caller's scope back
-- before it returns: an error on the way aborts the caller's transaction,
-- which takes the setting with it. A function-level SET clause would say the
-- same more simply, but setting a custom parameter there takes a superuser,
-- and the owner is not one.
--
-- A data migration from here on reads and writes under the scope Migrate
-- sets for its run; one run apart from Migrate sets limes.all_tenants itself,
-- or touches no row.

CREATE OR REPLACE FUNCTION limes.is_revoked(target uuid, max_depth integer)
RETURNS TABLE (self boolean, deepest integer, in_chain boolean)
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
        EXISTS (SELECT 1 FROM chain JOIN limes.revocations r ON r.capability_id = chain.id)
    FROM chain;
    PERFORM set_config('limes.all_tenants', caller_scope, true);
END
$$;

CREATE OR REPLACE FUNCTION limes.is_biscuit_revoked(revocation_ids bytea[])
RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    caller_scope text := coalesce(current_setting('limes.all_tenants', true), '');
    revoked boolean;
BEGIN
    PERFORM set_config('limes.all_tenants', 'read', true);
    SELECT EXISTS (SELECT 1 FROM limes.biscuit_revocations b WHERE b.revocation_id = ANY (revocation_ids))
    INTO revoked;
    PERFORM set_config('limes.all_tenants', caller_scope, true);
    RETURN revoked;
END
$$;

ALTER TABLE limes.capabilities FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.revocations FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.biscuit_revocations FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.usage FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.copy_usage FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.tenant_budgets FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.charges FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.reservations FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.erased_tenants FORCE ROW LEVEL SECURITY;
