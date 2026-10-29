-- What the meter and the erase read of a capability's record: its place in
-- the delegation tree, its tenant, its expiry and the ceilings of its
-- caveats. They read this view and never limes.capabilities, so the records
-- may change their own columns while it keeps its shape.
--
-- A plain projection of one table, so a FOR KEY SHARE through it locks the
-- record rows themselves, as the meter's hold against Purge needs; the walk
-- up the tree stays in the meter's queries. security_invoker makes the
-- forced row-level security of limes.capabilities bind the caller, as it
-- would reading the table, rather than the view's owner (PostgreSQL 15).
-- A record written before its caveats had copies has none: the zero quota.
CREATE VIEW limes.capability_lineage WITH (security_invoker = true) AS
SELECT
    id,
    parent_id,
    tenant_id,
    expires_at,
    capability -> 'caveats' -> 'requests' AS requests,
    capability -> 'caveats' -> 'budget' AS budget,
    coalesce(capability -> 'caveats' -> 'copies', '{}'::jsonb) AS copies
FROM limes.capabilities;
