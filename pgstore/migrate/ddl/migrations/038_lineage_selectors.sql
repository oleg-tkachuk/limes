-- The meter's release of what a bulk revocation left (ReleaseMatching, for
-- holds and leases) selects capabilities by whether they are revoked and by
-- the bulk revocation's selectors. It reads them through the view, as the
-- meter reads everything of a record (022), so the view gains those columns,
-- appended: the columns it had keep their names, types and order.
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
    run_id
FROM limes.capabilities;
