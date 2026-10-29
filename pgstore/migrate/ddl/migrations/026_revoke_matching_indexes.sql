---- tern: disable-tx ----
-- A bulk revocation finds a tenant's capabilities by subject, client,
-- on-behalf-of or session, in id order. Built CONCURRENTLY, holding no
-- writes (CONTRIBUTING.md); each dropped first, so a build that failed runs
-- again.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_subject;
CREATE INDEX CONCURRENTLY capabilities_tenant_subject ON limes.capabilities (tenant_id, subject, id);
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_client;
CREATE INDEX CONCURRENTLY capabilities_tenant_client ON limes.capabilities (tenant_id, client_id, id) WHERE client_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_on_behalf_of;
CREATE INDEX CONCURRENTLY capabilities_tenant_on_behalf_of ON limes.capabilities (tenant_id, on_behalf_of, id) WHERE on_behalf_of IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_session;
CREATE INDEX CONCURRENTLY capabilities_tenant_session ON limes.capabilities (tenant_id, session, id) WHERE session IS NOT NULL;
