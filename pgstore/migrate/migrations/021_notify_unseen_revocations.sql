-- A revocation of a capability the writer's row-level security hides is
-- notified with an empty payload, which clears a cache whole: 018 named
-- only the capabilities its join read, so a statement revoking none it
-- could see sent nothing, and a cache missed the revocation until its TTL.
-- Otherwise as 018: each capability revoked whose parent has no revocation
-- entry, past 100 (pgstore.MaxNotifiedRevocations) none, and none for a
-- Biscuit copy's revocation.
CREATE OR REPLACE FUNCTION limes.notify_revocation() RETURNS trigger
LANGUAGE plpgsql
-- Pinned again, as 007 pinned it: a replaced function keeps no setting.
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    named text[];
BEGIN
    IF NOT EXISTS (SELECT 1 FROM written) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME <> 'revocations'
       OR EXISTS (
           SELECT 1 FROM written w
           WHERE NOT EXISTS (SELECT 1 FROM limes.capabilities c WHERE c.id = w.capability_id)
       ) THEN
        PERFORM pg_notify('limes_revocations', '');
        RETURN NULL;
    END IF;
    SELECT array_agg(w.capability_id::text) INTO named
    FROM (
        SELECT w.capability_id FROM written w
        JOIN limes.capabilities c ON c.id = w.capability_id
        WHERE c.parent_id IS NULL
           OR NOT EXISTS (SELECT 1 FROM limes.revocations p WHERE p.capability_id = c.parent_id)
        LIMIT 101
    ) w;
    IF named IS NULL THEN
        RETURN NULL;
    END IF;
    IF cardinality(named) > 100 THEN
        PERFORM pg_notify('limes_revocations', '');
    ELSE
        PERFORM pg_notify('limes_revocations', array_to_string(named, ' '));
    END IF;
    RETURN NULL;
END
$$;
