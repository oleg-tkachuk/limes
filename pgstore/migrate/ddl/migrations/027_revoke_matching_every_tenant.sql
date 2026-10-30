---- tern: disable-tx ----
-- A bulk revocation across tenants finds a client's or a session's
-- capabilities in every tenant, a tenant's at a time and in id order
-- (docs/adr/0014-bulk-revocation-across-tenants.md). Built CONCURRENTLY,
-- holding no writes (CONTRIBUTING.md); each dropped first, so a build that
-- failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_client_tenant;
CREATE INDEX CONCURRENTLY capabilities_client_tenant ON limes.capabilities (client_id, tenant_id, id) WHERE client_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_session_tenant;
CREATE INDEX CONCURRENTLY capabilities_session_tenant ON limes.capabilities (session, tenant_id, id) WHERE session IS NOT NULL;
