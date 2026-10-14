-- Every statement that writes revocations, of capabilities or of Biscuit
-- copies, notifies channel limes_revocations (pgstore.RevocationChannel) once,
-- at commit; one that wrote none — a revocation already on record — does not. A verifier that caches revocation checks listens with
-- pgstore.RevocationWatcher and clears its caches, so a revocation reaches
-- every replica at once instead of within the cache TTL. The notification
-- carries nothing: a cache is cleared whole.

CREATE FUNCTION limes.notify_revocation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM written) THEN
        PERFORM pg_notify('limes_revocations', '');
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER notify_revocation AFTER INSERT ON limes.revocations
    REFERENCING NEW TABLE AS written FOR EACH STATEMENT EXECUTE FUNCTION limes.notify_revocation();

CREATE TRIGGER notify_revocation AFTER INSERT ON limes.biscuit_revocations
    REFERENCING NEW TABLE AS written FOR EACH STATEMENT EXECUTE FUNCTION limes.notify_revocation();
