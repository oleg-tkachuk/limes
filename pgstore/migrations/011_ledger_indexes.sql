-- SumCharges reads the charges of a period, of one tenant or of every
-- tenant: an index for each, so that a report reads the period's rows
-- rather than the whole ledger.

CREATE INDEX charges_tenant_at ON limes.charges (tenant_id, at);
CREATE INDEX charges_at ON limes.charges (at);
