-- A statement's revocations are notified in parts of up to 100 ids
-- (pgstore.MaxNotifiedRevocations), up to 1000 in all
-- (pgstore.MaxNotifiedRevocationsPerStatement): 021 sent one notification
-- of up to 100, so a bulk revocation of more cleared every cache whole.
-- Past 1000, as before, one notification names none. Otherwise as 021.
CREATE OR REPLACE FUNCTION limes.notify_revocation() RETURNS trigger
LANGUAGE plpgsql
-- Pinned again, as 007 pinned it: a replaced function keeps no setting.
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    named text[];
    part int;
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
        LIMIT 1001
    ) w;
    IF named IS NULL THEN
        RETURN NULL;
    END IF;
    IF cardinality(named) > 1000 THEN
        PERFORM pg_notify('limes_revocations', '');
        RETURN NULL;
    END IF;
    FOR part IN 0 .. (cardinality(named) - 1) / 100 LOOP
        PERFORM pg_notify('limes_revocations', array_to_string(named[part * 100 + 1 : part * 100 + 100], ' '));
    END LOOP;
    RETURN NULL;
END
$$;
