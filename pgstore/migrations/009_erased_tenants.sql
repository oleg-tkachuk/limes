-- A tenant whose usage EraseTenantUsage removed. Its records outlive the
-- erase, revoked, until Purge removes them, so a request verified before the
-- erase or a late cost could still be metered: the meter refuses a tenant
-- listed here (limes.ErrTenantErased), and PurgeOrphans removes the row once
-- none of the tenant's records is left.
CREATE TABLE limes.erased_tenants (
    tenant_id uuid        PRIMARY KEY,
    erased_at timestamptz NOT NULL
);

ALTER TABLE limes.erased_tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON limes.erased_tenants FOR SELECT
    USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.erased_tenants FOR INSERT
    WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.erased_tenants FOR DELETE
    USING (limes.tenant_writable(tenant_id));
