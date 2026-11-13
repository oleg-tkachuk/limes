-- A bump counted under an ExternalRef (meter.BumpRequest.ExternalRef), so a
-- bump sent again after its answer was lost returns the first receipt
-- rather than counting twice: the capability and the reference, the tenant
-- and the Biscuit copies it was counted for, which a delivery again must
-- match, the count it returned, and when. PurgeCharges removes a row older
-- than its retention, as it removes a charge; DeleteUsage removes the
-- capability's rows, whose counters restart, and an erase the tenant's.
-- Row-level security keys on the tenant, as limes.usage does.
CREATE TABLE limes.bumps (
    capability_id uuid        NOT NULL,
    external_ref  text        NOT NULL CHECK (external_ref <> ''),
    tenant_id     uuid        NOT NULL,
    copies        bytea[]     NOT NULL,
    request_count bigint      NOT NULL CHECK (request_count > 0),
    bumped_at     timestamptz NOT NULL,
    PRIMARY KEY (capability_id, external_ref)
);

-- PurgeCharges takes the oldest first, and an erase finds the tenant's.
CREATE INDEX bumps_bumped_at ON limes.bumps (bumped_at);
CREATE INDEX bumps_tenant ON limes.bumps (tenant_id);

ALTER TABLE limes.bumps ENABLE ROW LEVEL SECURITY;
ALTER TABLE limes.bumps FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON limes.bumps FOR SELECT
    USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.bumps FOR INSERT
    WITH CHECK (limes.tenant_writable(tenant_id));
-- No call updates a row; PurgeCharges locks those it removes, which takes
-- the update policy.
CREATE POLICY tenant_update ON limes.bumps FOR UPDATE
    USING (limes.tenant_writable(tenant_id)) WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.bumps FOR DELETE
    USING (limes.tenant_writable(tenant_id));
