-- Each record says whether it is revoked — itself or through an ancestor
-- still on record, as limes.is_revoked answers — so a listing or a census
-- reads it from the record rather than walking down from every revocation
-- entry of the tenant on every page.
--
-- Adding a column with a constant default rewrites no row: the table is
-- locked only for the moment the catalog changes. The records revoked
-- before the triggers below are marked by 016, in a transaction of its own,
-- so this lock is not held while they are.
ALTER TABLE limes.capabilities ADD COLUMN revoked boolean NOT NULL DEFAULT false;

-- A revocation entry marks its record and every record below it, a level
-- at a time: each level is marked by one statement, which waits for any
-- insert of a child holding its row FOR SHARE, and its children are read by
-- the next, whose snapshot then shows them (read committed, every store's
-- transaction). A child inserted after a level is marked waits on its
-- parent's row and inherits the mark (inherit_revoked). A record marked has
-- every record below it marked already, so the walk stops at one: a
-- cascading revoke, which writes an entry for each descendant after its
-- root's, marks the subtree once. Records deeper than MaxDelegationDepth are
-- never written, so the walk ends.
CREATE FUNCTION limes.mark_revoked() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    level uuid[] := ARRAY[NEW.capability_id];
BEGIN
    LOOP
        WITH marked AS (
            UPDATE limes.capabilities SET revoked = true
            WHERE id = ANY (level) AND NOT revoked
            RETURNING id
        )
        SELECT array_agg(id) INTO level FROM marked;
        EXIT WHEN level IS NULL;
        SELECT array_agg(id) INTO level FROM limes.capabilities
        WHERE parent_id = ANY (level) AND NOT revoked;
        EXIT WHEN level IS NULL;
    END LOOP;
    RETURN NULL;
END
$$;

CREATE TRIGGER revocations_mark_revoked
    AFTER INSERT ON limes.revocations
    FOR EACH ROW EXECUTE FUNCTION limes.mark_revoked();

-- A record is revoked from its insert when its parent is. Insert locks the
-- parent's row FOR SHARE, and marking it revoked takes the row for update,
-- so a record and its parent's revocation never pass each other.
CREATE FUNCTION limes.inherit_revoked() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    NEW.revoked := NEW.parent_id IS NOT NULL
        AND coalesce((SELECT revoked FROM limes.capabilities WHERE id = NEW.parent_id), false);
    RETURN NEW;
END
$$;

CREATE TRIGGER capabilities_inherit_revoked
    BEFORE INSERT ON limes.capabilities
    FOR EACH ROW EXECUTE FUNCTION limes.inherit_revoked();

-- PostgreSQL grants EXECUTE on a new function to every role (012).
REVOKE EXECUTE ON FUNCTION limes.mark_revoked(), limes.inherit_revoked() FROM PUBLIC;
