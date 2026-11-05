-- The identity provider a capability's subject was authenticated by: the
-- issuer of the subject token it was exchanged for
-- (limes.InsertRequest.SubjectIssuer), which a bulk revocation selects by so
-- that two providers naming the same client or session are told apart
-- (docs/adr/0014-bulk-revocation-across-tenants.md). A real column, as
-- 025's are, since row-level security keeps a policy's filter from reading
-- a jsonb member through an index; a child holds its parent's.
--
-- Filled in where an exchange recorded the subject token's issuer
-- (limes.exchanges, 024), for the capability it issued and every one
-- delegated from it. Any other record keeps NULL, and a revocation by
-- subject issuer misses it. The walk stops at a record it has been through,
-- so a parent chain that loops ends.
ALTER TABLE limes.capabilities ADD COLUMN subject_issuer text;

WITH RECURSIVE issued AS (
    SELECT e.capability_id AS id, e.issuer FROM limes.exchanges e
    UNION ALL
    SELECT c.id, issued.issuer FROM limes.capabilities c JOIN issued ON c.parent_id = issued.id
) CYCLE id SET looped USING path
UPDATE limes.capabilities c
SET subject_issuer = issued.issuer
FROM issued
WHERE c.id = issued.id AND NOT issued.looped;

-- The meter's release of what a bulk revocation left selects by it through
-- the lineage view, as by every other selector (038): appended, so the
-- columns the view had keep their names, types and order.
CREATE OR REPLACE VIEW limes.capability_lineage WITH (security_invoker = true) AS
SELECT
    id,
    parent_id,
    tenant_id,
    expires_at,
    capability -> 'caveats' -> 'requests' AS requests,
    capability -> 'caveats' -> 'budget' AS budget,
    coalesce(capability -> 'caveats' -> 'copies', '{}'::jsonb) AS copies,
    revoked,
    subject,
    on_behalf_of,
    client_id,
    session,
    run_id,
    subject_issuer
FROM limes.capabilities;
