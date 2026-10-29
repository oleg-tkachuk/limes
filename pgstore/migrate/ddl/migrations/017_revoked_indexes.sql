---- tern: disable-tx ----
-- A listing of the records of some dispositions reads the tenant's records
-- of the revoked flag those dispositions have, in id order, rather than
-- passing over the others: active and expired records are not revoked.
-- Built CONCURRENTLY, holding no writes (CONTRIBUTING.md); each dropped
-- first, so a build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_unrevoked;
CREATE INDEX CONCURRENTLY capabilities_tenant_unrevoked ON limes.capabilities (tenant_id, id) WHERE NOT revoked;
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_revoked;
CREATE INDEX CONCURRENTLY capabilities_tenant_revoked ON limes.capabilities (tenant_id, id) WHERE revoked;
