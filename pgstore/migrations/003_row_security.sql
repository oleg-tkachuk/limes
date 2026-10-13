-- Row-level security: a role that does not own the tables sees and writes
-- only the rows of the tenant its transaction is scoped to, or every row
-- when the transaction is scoped to all tenants. A transaction scoped to
-- neither sees nothing. pgstore scopes each transaction from the call's
-- context (pgstore.WithTenant, pgstore.WithAllTenants) through two settings
-- local to it. The owner, which migrates, is not bound.

CREATE FUNCTION limes.tenant_visible(tenant uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(current_setting('limes.all_tenants', true) = 'on', false)
        OR tenant = nullif(current_setting('limes.tenant_id', true), '')::uuid
$$;

ALTER TABLE limes.capabilities ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.capabilities
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.revocations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.revocations
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.biscuit_revocations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.biscuit_revocations
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.usage ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.usage
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.copy_usage ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.copy_usage
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.tenant_budgets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.tenant_budgets
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.charges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.charges
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));

ALTER TABLE limes.reservations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON limes.reservations
    USING (limes.tenant_visible(tenant_id)) WITH CHECK (limes.tenant_visible(tenant_id));
