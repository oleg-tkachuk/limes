-- Capability records, their revocation entries and the revoked Biscuit
-- copies: what limes.Store and limes.BiscuitRevocationStore keep.

-- Migrate creates the schema, limes, before it runs any migration.

-- One row per capability Insert recorded. The columns are what the store
-- queries by; capability and issued_by hold the rest, as Insert was given it.
-- depth counts the ancestors on record, fixed at insert: a parent outlives its
-- children on record, so it never changes. Purge goes deepest first by it.
CREATE TABLE limes.capabilities (
    id             uuid        PRIMARY KEY,
    parent_id      uuid        REFERENCES limes.capabilities (id),
    tenant_id      uuid        NOT NULL,
    principal_type text        NOT NULL,
    subject        text        NOT NULL,
    -- NULL when the capability has no expiry: it is never purged, and a
    -- listing counts it as expired, as limes reads a zero ExpiresAt.
    expires_at     timestamptz,
    depth          integer     NOT NULL CHECK (depth >= 0),
    capability     jsonb       NOT NULL,
    issued_by      jsonb       NOT NULL
);

CREATE INDEX capabilities_principal ON limes.capabilities (tenant_id, principal_type, subject, id);
CREATE INDEX capabilities_parent ON limes.capabilities (parent_id);
CREATE INDEX capabilities_purge ON limes.capabilities (expires_at, depth DESC, id)
    WHERE expires_at IS NOT NULL;

-- A capability's own revocation entry: the first Revoke's stands.
CREATE TABLE limes.revocations (
    capability_id uuid        PRIMARY KEY REFERENCES limes.capabilities (id) ON DELETE CASCADE,
    tenant_id     uuid        NOT NULL,
    revoked_at    timestamptz NOT NULL,
    note          text        NOT NULL,
    actor         text        NOT NULL,
    cascaded      boolean     NOT NULL
);

-- A revoked Biscuit copy, under the capability it belongs to.
CREATE TABLE limes.biscuit_revocations (
    revocation_id bytea       PRIMARY KEY CHECK (length(revocation_id) > 0),
    capability_id uuid        NOT NULL REFERENCES limes.capabilities (id) ON DELETE CASCADE,
    tenant_id     uuid        NOT NULL,
    revoked_at    timestamptz NOT NULL,
    note          text        NOT NULL,
    actor         text        NOT NULL
);

CREATE INDEX biscuit_revocations_capability ON limes.biscuit_revocations (capability_id);
