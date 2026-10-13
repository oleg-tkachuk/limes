-- The meter: counters, holds, the charges ledger and the tenant aggregates,
-- what limes.UsageStore keeps. Amounts are limes.Nanos, exact in bigint.
-- None of these tables refers to limes.capabilities: usage outlives a purged
-- record until PurgeOrphans takes it, and the ledger outlives both.

-- A capability's counters, its delegated children's included.
CREATE TABLE limes.usage (
    capability_id uuid   PRIMARY KEY,
    tenant_id     uuid   NOT NULL,
    request_count bigint NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    spent         bigint NOT NULL DEFAULT 0 CHECK (spent >= 0),
    reserved      bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    -- Empty until an amount is counted.
    unit_code     text   NOT NULL DEFAULT ''
);

-- A Biscuit copy's own counters, under its revocation id.
CREATE TABLE limes.copy_usage (
    revocation_id bytea  PRIMARY KEY CHECK (length(revocation_id) > 0),
    capability_id uuid   NOT NULL,
    tenant_id     uuid   NOT NULL,
    request_count bigint NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    spent         bigint NOT NULL DEFAULT 0 CHECK (spent >= 0),
    reserved      bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0)
);

CREATE INDEX copy_usage_capability ON limes.copy_usage (capability_id);

-- A tenant's aggregate. period counts its accounting periods: SetTenantBudget
-- with ResetSpend starts the next, and a charge records the one it was
-- counted in, so a refund leaves a later period's spend alone.
CREATE TABLE limes.tenant_budgets (
    tenant_id         uuid        PRIMARY KEY,
    ceiling_max       bigint      NOT NULL CHECK (ceiling_max >= 0),
    ceiling_unlimited boolean     NOT NULL,
    spent             bigint      NOT NULL DEFAULT 0 CHECK (spent >= 0),
    reserved          bigint      NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    unit_code         text        NOT NULL,
    period_start      timestamptz NOT NULL,
    -- NULL for an open-ended period.
    period_end        timestamptz,
    updated_at        timestamptz NOT NULL,
    resource_version  bigint      NOT NULL DEFAULT 0 CHECK (resource_version >= 0),
    period            bigint      NOT NULL DEFAULT 0 CHECK (period >= 0)
);

-- One committed charge. ancestors and copies are the counters it debited,
-- nearest and innermost first, so a refund returns to exactly those.
CREATE TABLE limes.charges (
    id             uuid        PRIMARY KEY,
    capability_id  uuid        NOT NULL,
    tenant_id      uuid        NOT NULL,
    amount         bigint      NOT NULL CHECK (amount >= 0),
    refunded       bigint      NOT NULL DEFAULT 0 CHECK (refunded >= 0 AND refunded <= amount),
    unit_code      text        NOT NULL,
    op             text        NOT NULL,
    actor          text        NOT NULL,
    at             timestamptz NOT NULL,
    ancestors      uuid[]      NOT NULL,
    copies         bytea[]     NOT NULL,
    -- Empty for a charge taken under no reference.
    external_ref   text        NOT NULL DEFAULT '',
    -- NULL for a charge made directly, not by a Settle.
    reservation_id uuid,
    overrun        boolean     NOT NULL,
    tenant_period  bigint      NOT NULL
);

-- What makes a charge idempotent: one per reference, one per settled hold.
CREATE UNIQUE INDEX charges_external_ref ON limes.charges (capability_id, external_ref)
    WHERE external_ref <> '';
CREATE UNIQUE INDEX charges_reservation ON limes.charges (reservation_id)
    WHERE reservation_id IS NOT NULL;

-- An open hold. ancestors are those it holds against, as they were when it
-- was made; one whose counters are dropped since is struck from the list.
-- copies are the Biscuit copies it is on, with their ceilings, as jsonb.
CREATE TABLE limes.reservations (
    id            uuid        PRIMARY KEY,
    capability_id uuid        NOT NULL,
    tenant_id     uuid        NOT NULL,
    ancestors     uuid[]      NOT NULL,
    amount        bigint      NOT NULL CHECK (amount >= 0),
    unit_code     text        NOT NULL,
    op            text        NOT NULL,
    actor         text        NOT NULL,
    copies        jsonb       NOT NULL,
    expires_at    timestamptz NOT NULL
);

CREATE INDEX reservations_capability ON limes.reservations (capability_id, expires_at, id);
CREATE INDEX reservations_expiry ON limes.reservations (expires_at);
CREATE INDEX reservations_ancestors ON limes.reservations USING gin (ancestors);
