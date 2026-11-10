package limes

import (
	"context"
	"time"
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

// ClaimExchangeRequest is ExchangeStore.ClaimExchange's request: the
// capability an exchange of the subject token Key names issued, and when
// that token expires.
type ClaimExchangeRequest struct {
	Key ExchangeKey
	// TokenExpiresAt is the subject token's exp: the record is kept until
	// then, whatever becomes of its capability, since the token can be
	// presented again until then.
	TokenExpiresAt time.Time
	// CapabilityID is the capability the exchange issued.
	CapabilityID uuid.UUID
}

// Validate refuses what ExchangeKey.Validate refuses, and a request with no
// TokenExpiresAt.
func (r ClaimExchangeRequest) Validate() error {
	if err := r.Key.Validate(); err != nil {
		return err
	}
	if r.TokenExpiresAt.IsZero() {
		return refuseRequest("ClaimExchangeRequest.TokenExpiresAt required")
	}
	return nil
}

// Validate refuses a key without a tenant, an issuer or a token id.
func (k ExchangeKey) Validate() error {
	switch {
	case k.TenantID == uuid.Nil():
		return refuseRequest("ExchangeKey.TenantID required")
	case k.Issuer == "":
		return refuseRequest("ExchangeKey.Issuer required")
	case k.TokenID == "":
		return refuseRequest("ExchangeKey.TokenID required")
	}
	return checkText("ExchangeKey", k.Issuer, k.TokenID)
}

// ExchangeStore records which capability exchanging a subject token issued,
// so that one subject token yields one capability across every replica.
// It holds the capability's id and nothing else: no token is stored, and the
// token is signed again from the record (Issuer.Reissue).
type ExchangeStore interface {
	// ExchangedFor is the capability recorded for key, or ErrNotFound when
	// there is none. It refuses what ExchangeKey.Validate refuses.
	ExchangedFor(ctx context.Context, key ExchangeKey) (uuid.UUID, error)

	// ClaimExchange records req.CapabilityID as the capability issued for
	// req.Key, and returns it with claimed true. When the key holds a
	// capability already it records nothing and returns that one with
	// claimed false, so concurrent claims of one key converge on one id. The
	// capability must be on record in the key's tenant: ErrNotFound for one
	// that is not, or is another tenant's. The record outlives its
	// capability: Purge and an erase remove it only once req.TokenExpiresAt
	// is past their retention, so a token exchanged once is never exchanged
	// afresh while it can still be presented, and its capability purged
	// answers as gone (Issuer.Reissue: ErrNotFound). It refuses what
	// ClaimExchangeRequest.Validate refuses.
	ClaimExchange(ctx context.Context, req ClaimExchangeRequest) (recorded uuid.UUID, claimed bool, err error)
}
