---- tern: disable-tx ----
-- A bulk revocation finds an identity provider's capabilities, in every
-- tenant or in one, a tenant's at a time and in id order. One index serves
-- both: within a tenant the issuer and the tenant are equalities, so the
-- tenant's rows lie together in id order, as 027's do for a client. Built
-- CONCURRENTLY, holding no writes (CONTRIBUTING.md); dropped first, so a
-- build that failed runs again.
DROP INDEX CONCURRENTLY IF EXISTS limes.capabilities_subject_issuer_tenant;
CREATE INDEX CONCURRENTLY capabilities_subject_issuer_tenant ON limes.capabilities (subject_issuer, tenant_id, id) WHERE subject_issuer IS NOT NULL;
