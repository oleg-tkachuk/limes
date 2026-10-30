---- tern: disable-tx ----
-- A bulk revocation finds a tenant's capabilities of an agent run in id
-- order. Built CONCURRENTLY, holding no writes (CONTRIBUTING.md); dropped
-- first, so a build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_run;
CREATE INDEX CONCURRENTLY capabilities_tenant_run ON limes.capabilities (tenant_id, run_id, id) WHERE run_id IS NOT NULL;
