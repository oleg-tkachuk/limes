-- A lease of requests beside its budget (meter.Leaser,
-- docs/adr/0017-budget-leases.md): lease_requests held against the row's
-- own counters and each of lease_ancestors, of which lease_requests_spent
-- are made. reserved_requests is what the leases of a row's capability, and
-- of those below it, hold of it: a Bump not under a lease counts them
-- against the ceiling as made.
ALTER TABLE limes.usage
    ADD COLUMN reserved_requests    bigint NOT NULL DEFAULT 0 CHECK (reserved_requests >= 0),
    ADD COLUMN lease_requests       bigint NOT NULL DEFAULT 0 CHECK (lease_requests >= 0),
    ADD COLUMN lease_requests_spent bigint NOT NULL DEFAULT 0 CHECK (lease_requests_spent >= 0);
