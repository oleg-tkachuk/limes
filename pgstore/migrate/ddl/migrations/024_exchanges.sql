-- The capability each subject token's exchange issued, for an Exchanger that
-- issues one capability per subject token (limes.ExchangeStore,
-- docs/adr/0009-one-capability-per-subject-token.md): the token's issuer and
-- jti within the tenant, and the capability's id. No token is kept: the
-- token is signed again from the capability's record. A row goes with its
-- capability, when Purge or an erase removes it.
CREATE TABLE limes.exchanges (
    tenant_id     uuid NOT NULL,
    issuer        text NOT NULL,
    token_id      text NOT NULL,
    capability_id uuid NOT NULL UNIQUE REFERENCES limes.capabilities (id) ON DELETE CASCADE,
    PRIMARY KEY (tenant_id, issuer, token_id)
);

ALTER TABLE limes.exchanges ENABLE ROW LEVEL SECURITY;
ALTER TABLE limes.exchanges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON limes.exchanges FOR SELECT
    USING (limes.tenant_visible(tenant_id));
CREATE POLICY tenant_insert ON limes.exchanges FOR INSERT
    WITH CHECK (limes.tenant_writable(tenant_id));
CREATE POLICY tenant_delete ON limes.exchanges FOR DELETE
    USING (limes.tenant_writable(tenant_id));
