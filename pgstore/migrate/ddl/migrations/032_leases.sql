-- A capability's lease of its chain's budget (meter.Leaser,
-- docs/adr/0017-budget-leases.md), on its usage row, so it is locked with
-- its counters in the order every call takes: lease_amount held against its
-- own counters, each of lease_ancestors and its tenant's, of which
-- lease_spent is spent; lease_period the tenant's period it opened in. A row
-- with no lease has a NULL lease_id.
ALTER TABLE limes.usage
    ADD COLUMN lease_id        uuid,
    ADD COLUMN lease_amount    bigint      NOT NULL DEFAULT 0 CHECK (lease_amount >= 0),
    ADD COLUMN lease_spent     bigint      NOT NULL DEFAULT 0 CHECK (lease_spent >= 0),
    ADD COLUMN lease_expires   timestamptz,
    ADD COLUMN lease_ancestors uuid[]      NOT NULL DEFAULT '{}',
    ADD COLUMN lease_period    bigint      NOT NULL DEFAULT 0;

-- A charge taken under a lease names it and its leaseholder, so Refund
-- returns it to the lease while it is open, and to the chain once settled.
ALTER TABLE limes.charges
    ADD COLUMN lease_id     uuid,
    ADD COLUMN lease_holder uuid;
