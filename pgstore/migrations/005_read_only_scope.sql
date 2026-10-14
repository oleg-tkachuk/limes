-- Reading every tenant apart from writing every tenant. limes.all_tenants is
-- 'on' in a transaction scoped to every tenant, to read and write
-- (pgstore.WithAllTenants), and 'read' in one that may read every tenant's
-- rows and write none (pgstore.WithAllTenantsReadOnly). One policy per table
-- read both alike; each table now has one policy for reading and one for each
-- kind of write.

-- tenant_visible: may the transaction read tenant's rows.
CREATE OR REPLACE FUNCTION limes.tenant_visible(tenant uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(current_setting('limes.all_tenants', true) IN ('on', 'read'), false)
        OR tenant = nullif(current_setting('limes.tenant_id', true), '')::uuid
$$;

-- tenant_writable: may the transaction write tenant's rows.
CREATE FUNCTION limes.tenant_writable(tenant uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT coalesce(current_setting('limes.all_tenants', true) = 'on', false)
        OR tenant = nullif(current_setting('limes.tenant_id', true), '')::uuid
$$;

DROP POLICY tenant_scope ON limes.capabilities;
CREATE POLICY tenant_read ON limes.capabilities FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.capabilities FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.capabilities FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.capabilities FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.revocations;
CREATE POLICY tenant_read ON limes.revocations FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.revocations FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.revocations FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.revocations FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.biscuit_revocations;
CREATE POLICY tenant_read ON limes.biscuit_revocations FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.biscuit_revocations FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.biscuit_revocations FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.biscuit_revocations FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.usage;
CREATE POLICY tenant_read ON limes.usage FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.usage FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.usage FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.usage FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.copy_usage;
CREATE POLICY tenant_read ON limes.copy_usage FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.copy_usage FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.copy_usage FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.copy_usage FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.tenant_budgets;
CREATE POLICY tenant_read ON limes.tenant_budgets FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.tenant_budgets FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.tenant_budgets FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.tenant_budgets FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.charges;
CREATE POLICY tenant_read ON limes.charges FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.charges FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.charges FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.charges FOR DELETE USING (limes.tenant_writable(tenant_id));

DROP POLICY tenant_scope ON limes.reservations;
CREATE POLICY tenant_read ON limes.reservations FOR SELECT USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.reservations FOR INSERT WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.reservations FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.reservations FOR DELETE USING (limes.tenant_writable(tenant_id));
