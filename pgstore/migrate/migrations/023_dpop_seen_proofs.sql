-- The DPoP proof ids a verifier has accepted, for pgstore.ReplayCache: one
-- table every replica shares, so a proof accepted on one is refused on all
-- of them until it expires.
--
-- An id is its key's thumbprint and the proof's jti. holder is the
-- capability whose share of the cache the id counts against. A row past
-- expires_at counts for nothing, and the same id may take its place; Purge
-- of the cache deletes such rows in batches.
--
-- No row-level security: the check runs before any tenant is known, as the
-- runtime role, and the table holds no tenant's data.
CREATE TABLE limes.dpop_seen_proofs (
    jkt        text        NOT NULL,
    jti        text        NOT NULL,
    holder     text        NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (jkt, jti)
);

CREATE INDEX dpop_seen_proofs_expires ON limes.dpop_seen_proofs (expires_at);
CREATE INDEX dpop_seen_proofs_holder ON limes.dpop_seen_proofs (holder, expires_at);
