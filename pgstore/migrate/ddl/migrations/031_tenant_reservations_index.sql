---- tern: disable-tx ----
-- An operator lists a tenant's holds, soonest to expire first. Built
-- CONCURRENTLY, holding no writes (CONTRIBUTING.md); dropped first, so a
-- build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.reservations_tenant;
CREATE INDEX CONCURRENTLY reservations_tenant ON limes.reservations (tenant_id, expires_at, id);
