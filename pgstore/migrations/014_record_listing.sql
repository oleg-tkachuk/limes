---- tern: disable-tx ----
-- Census.ListRecords pages one tenant's records in id order, either way,
-- whatever principal type or subject it filters by: an index on the
-- tenant's ids, so a page reads the rows it lists — and those its filters
-- pass over — rather than every record of the tenant sorted. A listing of
-- one principal type reads its own, the narrowing a listing most often
-- asks for. capabilities_principal still serves a listing of one subject.
--
-- The census and the listing start the walk down to every revoked record
-- from the tenant's own revocation entries: an index on them, so that start
-- reads the tenant's entries rather than every tenant's.
--
-- Built CONCURRENTLY, holding no writes (CONTRIBUTING.md), each dropped
-- first, so a build that failed runs again. Its first form built the same
-- three indexes with a plain CREATE INDEX, holding writes while it ran; a
-- store that ran that one has them already, and tern, which keeps a version
-- and no checksum, runs 014 there no more.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_id;
CREATE INDEX CONCURRENTLY capabilities_tenant_id ON limes.capabilities (tenant_id, id);
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_tenant_type_id;
CREATE INDEX CONCURRENTLY capabilities_tenant_type_id ON limes.capabilities (tenant_id, principal_type, id);
DROP INDEX CONCURRENTLY IF EXISTS limes.revocations_tenant;
CREATE INDEX CONCURRENTLY revocations_tenant ON limes.revocations (tenant_id);
