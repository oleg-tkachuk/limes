-- The stripes of a tenant's aggregate (docs/adr/0022-unlimited-tenant-aggregate.md):
-- what the calls of a tenant with no ceiling added to its spend and holds
-- since the aggregate was last folded, one row per stripe, so that those
-- calls lock a stripe rather than the one aggregate row. The aggregate is
-- its row plus the sum of its stripes. The deltas are signed: a settle or a
-- refund may subtract on another stripe than the charge or the hold added
-- on, so a stripe may go negative while the sum does not. A stripe holds
-- only the current period's deltas; period guards the fold, which asserts it
-- against the row's. Row-level security keys on the tenant, as
-- limes.tenant_budgets does.
CREATE TABLE limes.tenant_budget_stripes (
    tenant_id  uuid        NOT NULL,
    stripe     integer     NOT NULL,
    period     bigint      NOT NULL,
    spent      bigint      NOT NULL,
    reserved   bigint      NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, stripe)
);

ALTER TABLE limes.tenant_budget_stripes ENABLE ROW LEVEL SECURITY;
ALTER TABLE limes.tenant_budget_stripes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON limes.tenant_budget_stripes FOR SELECT
    USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.tenant_budget_stripes FOR INSERT
    WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_update ON limes.tenant_budget_stripes FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.tenant_budget_stripes FOR DELETE
    USING (limes.tenant_writable(tenant_id));
