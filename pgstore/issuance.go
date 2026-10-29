package pgstore

import (
	"context"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// ScopedIssuance is s as the limes.IssuanceStore of an Issuer that issues
// where no tenant is in scope — behind a token endpoint, whose requests name
// none: Insert runs scoped to the tenant of the capability it records, as
// WithTenant scopes it, so row-level security admits the record whatever
// the caller's context says. IsRevoked sees every tenant already, and
// GetRecord reads every tenant's, read-only, for Issuer.Reissue. It scopes
// through the context, so a Config.Scope of the consumer's that does not
// read WithTenant gains nothing from it.
func ScopedIssuance(s *Store) limes.IssuanceStore { return scopedIssuance{s} }

// scopedIssuance is ScopedIssuance's store.
type scopedIssuance struct{ *Store }

// Insert records req under the scope of its capability's tenant.
func (s scopedIssuance) Insert(ctx context.Context, req limes.InsertRequest) error {
	return s.Store.Insert(WithTenant(ctx, req.Capability.Subject.TenantID), req)
}

// GetRecord reads a record across every tenant, read-only, for
// Issuer.Reissue: the issuer reissues a capability by its id alone, whose
// tenant it does not know before it reads it, and writes nothing.
func (s scopedIssuance) GetRecord(ctx context.Context, id uuid.UUID) (limes.Record, error) {
	return s.Store.GetRecord(WithAllTenantsReadOnly(ctx), id)
}
