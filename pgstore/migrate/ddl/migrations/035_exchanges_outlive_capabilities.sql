-- An exchange's record outlives its capability (limes.ExchangeStore,
-- docs/adr/0009-one-capability-per-subject-token.md): its subject token can
-- be presented again until it expires, so Purge keeps the row until then
-- and a purged capability answers as gone rather than letting the token be
-- exchanged afresh. Rows already recorded take their capability's expiry,
-- the closest bound on record. The table forces row-level security and has
-- no UPDATE policy, so the backfill runs with it relaxed for the owner,
-- inside this step's transaction.
ALTER TABLE limes.exchanges DROP CONSTRAINT exchanges_capability_id_fkey;
ALTER TABLE limes.exchanges ADD COLUMN token_expires_at timestamptz;
ALTER TABLE limes.exchanges NO FORCE ROW LEVEL SECURITY;
UPDATE limes.exchanges e
SET token_expires_at = coalesce(c.expires_at, now())
FROM limes.capabilities c
WHERE c.id = e.capability_id;
UPDATE limes.exchanges SET token_expires_at = now() WHERE token_expires_at IS NULL;
ALTER TABLE limes.exchanges FORCE ROW LEVEL SECURITY;
ALTER TABLE limes.exchanges ALTER COLUMN token_expires_at SET NOT NULL;
