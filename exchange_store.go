package limes

import (
	"context"
	"uuid"
)

// ExchangeKey names one subject token an exchange issues one capability
// for (docs/adr/0009-one-capability-per-subject-token.md): its issuer and
// jti, within the tenant the capability is issued in.
type ExchangeKey struct {
	TenantID uuid.UUID
	// Issuer is the subject token's iss, and TokenID its jti.
	Issuer, TokenID string
}

// Validate refuses a key without a tenant, an issuer or a token id.
func (k ExchangeKey) Validate() error {
	switch {
	case k.TenantID == uuid.Nil():
		return invalidRequest("ExchangeKey.TenantID required")
	case k.Issuer == "":
		return invalidRequest("ExchangeKey.Issuer required")
	case k.TokenID == "":
		return invalidRequest("ExchangeKey.TokenID required")
	}
	return nil
}

// ExchangeStore records which capability exchanging a subject token issued,
// so that one subject token yields one capability across every replica.
// It holds the capability's id and nothing else: no token is stored, and the
// token is signed again from the record (Issuer.Reissue).
type ExchangeStore interface {
	// ExchangedFor is the capability recorded for key, or ErrNotFound when
	// there is none. It refuses what ExchangeKey.Validate refuses.
	ExchangedFor(ctx context.Context, key ExchangeKey) (uuid.UUID, error)

	// ClaimExchange records id as the capability issued for key, and
	// returns it with claimed true. When key holds a capability already it
	// records nothing and returns that one with claimed false, so concurrent
	// claims of one key converge on one id. id must be on record in key's
	// tenant: ErrNotFound for one that is not, or is another tenant's. The
	// mapping goes with its capability when Purge removes it. It refuses
	// what ExchangeKey.Validate refuses.
	ClaimExchange(ctx context.Context, key ExchangeKey, id uuid.UUID) (recorded uuid.UUID, claimed bool, err error)
}
