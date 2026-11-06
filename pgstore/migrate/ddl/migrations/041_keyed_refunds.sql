-- A refund made under a RefundID (meter.RefundRequest.RefundID), so a
-- refund sent again after its answer was lost returns the first receipt
-- rather than refunding twice: the id, the charge it refunded, what it asked
-- for — an amount, or the remainder — and what it returned. A row goes with
-- its charge, when PurgeCharges or an erase removes it, so the key lasts as
-- long as the charge does. Row-level security keys on the charge's tenant,
-- as limes.charges does.
CREATE TABLE limes.refunds (
    id         uuid        PRIMARY KEY,
    charge_id  uuid        NOT NULL REFERENCES limes.charges (id) ON DELETE CASCADE,
    tenant_id  uuid        NOT NULL,
    -- What the request asked for: an amount, or 0 with remaining.
    amount     bigint      NOT NULL CHECK (amount >= 0),
    remaining  boolean     NOT NULL,
    -- What the refund returned.
    refunded   bigint      NOT NULL CHECK (refunded >= 0),
    created_at timestamptz NOT NULL
);

-- A charge's delete finds its refunds through it.
CREATE INDEX refunds_charge ON limes.refunds (charge_id);

ALTER TABLE limes.refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE limes.refunds FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON limes.refunds FOR SELECT
    USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.refunds FOR INSERT
    WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.refunds FOR DELETE
    USING (limes.tenant_writable(tenant_id));
