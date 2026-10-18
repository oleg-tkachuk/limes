-- The policy functions of 003_row_security.sql and 005_read_only_scope.sql
-- name the catalog's functions, operators and types in full, so the search
-- path of the session that runs them cannot put a look-alike in front of
-- pg_catalog and answer for them. They stay plain SQL with no SET clause,
-- which the planner would not inline: a policy that inlines keeps the
-- tenant_id indexes usable. The trigger function of
-- 006_revocation_notify.sql is plpgsql, never inlined, and pins its search
-- path instead.

CREATE OR REPLACE FUNCTION limes.tenant_visible(tenant pg_catalog.uuid) RETURNS pg_catalog.bool
LANGUAGE sql STABLE AS $$
    SELECT coalesce(pg_catalog.current_setting('limes.all_tenants', true) OPERATOR(pg_catalog.=) ANY (ARRAY['on', 'read']::pg_catalog.text[]), false)
        OR tenant OPERATOR(pg_catalog.=) nullif(pg_catalog.current_setting('limes.tenant_id', true), '')::pg_catalog.uuid
$$;

CREATE OR REPLACE FUNCTION limes.tenant_writable(tenant pg_catalog.uuid) RETURNS pg_catalog.bool
LANGUAGE sql STABLE AS $$
    SELECT coalesce(pg_catalog.current_setting('limes.all_tenants', true) OPERATOR(pg_catalog.=) 'on', false)
        OR tenant OPERATOR(pg_catalog.=) nullif(pg_catalog.current_setting('limes.tenant_id', true), '')::pg_catalog.uuid
$$;

ALTER FUNCTION limes.notify_revocation() SET search_path = pg_catalog, pg_temp;
