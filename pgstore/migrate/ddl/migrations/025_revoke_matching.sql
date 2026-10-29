-- What a bulk revocation selects a tenant's capabilities by
-- (limes.BulkRevoker, docs/adr/0013-bulk-revocation.md): the client a
-- capability was granted to, the principal it acts for, and the identity
-- provider's session it was issued under. Real columns, not the capability
-- document's members: row-level security keeps a policy's filter from
-- reading a jsonb member through an index (->> is not leakproof).
--
-- client_id and on_behalf_of are filled in from the document for the
-- records already there; a session was never recorded, so those records
-- have none, and a revocation by session misses them.
ALTER TABLE limes.capabilities
    ADD COLUMN client_id    text,
    ADD COLUMN on_behalf_of text,
    ADD COLUMN session      text;

UPDATE limes.capabilities
SET client_id    = NULLIF(capability->>'client_id', ''),
    on_behalf_of = capability->'on_behalf_of'->>'subject'
WHERE capability->>'client_id' <> '' OR capability->'on_behalf_of' IS NOT NULL;
