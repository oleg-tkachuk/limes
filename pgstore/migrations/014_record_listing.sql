-- Census.ListRecords pages one tenant's records in id order, either way,
-- whatever principal type or subject it filters by: an index on the
-- tenant's ids, so a page reads the rows it lists — and those its filters
-- pass over — rather than every record of the tenant sorted. A listing of
-- one principal type reads its own, the narrowing a listing most often
-- asks for. capabilities_principal still serves a listing of one subject.
CREATE INDEX capabilities_tenant_id ON limes.capabilities (tenant_id, id);
CREATE INDEX capabilities_tenant_type_id ON limes.capabilities (tenant_id, principal_type, id);

-- The census and the listing start the walk down to every revoked record
-- from the tenant's own revocation entries: an index on them, so that start
-- reads the tenant's entries rather than every tenant's.
CREATE INDEX revocations_tenant ON limes.revocations (tenant_id);
