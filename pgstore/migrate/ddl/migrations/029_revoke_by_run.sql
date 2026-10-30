-- What a bulk revocation selects an agent run's capabilities by
-- (limes.RevokeMatchingRequest.RunID): the run the capability was issued to,
-- a real column as 025's are, since row-level security keeps a policy's
-- filter from reading a jsonb member through an index.
--
-- Filled in from each record's document; a record of no agent, or of an
-- agent that names no run, keeps NULL.
ALTER TABLE limes.capabilities ADD COLUMN run_id uuid;

UPDATE limes.capabilities
SET run_id = NULLIF(capability->'subject'->'agent'->>'run_id', '00000000-0000-0000-0000-000000000000')::uuid
WHERE capability->'subject'->'agent'->>'run_id' IS NOT NULL;
