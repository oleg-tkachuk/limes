-- A Biscuit copy's revocation id is its block's signature, which biscuit-go
-- makes without the signature of the block before it: a holder of two
-- Biscuits can graft a block of one onto the other, so one id can turn up on
-- chains of two capabilities. A copy's counters and its revocation are each
-- that capability's own, so that metering, refunding, purging or erasing one
-- capability never reaches another's. is_biscuit_revoked still matches an id
-- under any capability.

ALTER TABLE limes.copy_usage
    DROP CONSTRAINT copy_usage_pkey,
    ADD PRIMARY KEY (capability_id, revocation_id);

-- The primary key now leads with capability_id.
DROP INDEX limes.copy_usage_capability;

-- revocation_id leads: is_biscuit_revoked looks a copy up by its id alone.
ALTER TABLE limes.biscuit_revocations
    DROP CONSTRAINT biscuit_revocations_pkey,
    ADD PRIMARY KEY (revocation_id, capability_id);
