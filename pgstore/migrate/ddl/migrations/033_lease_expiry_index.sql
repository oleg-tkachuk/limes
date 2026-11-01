---- tern: disable-tx ----
-- ReleaseExpired settles the leases past their end, soonest first: the few
-- rows that hold one. Built CONCURRENTLY, holding no writes
-- (CONTRIBUTING.md); dropped first, so a build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.usage_lease_expires;
CREATE INDEX CONCURRENTLY usage_lease_expires ON limes.usage (lease_expires, capability_id) WHERE lease_id IS NOT NULL;
