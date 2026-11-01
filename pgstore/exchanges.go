package pgstore

import (
	"context"
	"errors"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/oleg-tkachuk/limes"
)

// The statements of limes.ExchangeStore (migrate/ddl/migrations/024_exchanges.sql,
// 035_exchanges_outlive_capabilities.sql).
const (
	sqlExchangedFor = `
SELECT capability_id FROM limes.exchanges WHERE tenant_id = $1 AND issuer = $2 AND token_id = $3`

	// sqlCapabilityOfTenant is whether capability $2 is on record in tenant
	// $1: a claim names one of the key's tenant.
	sqlCapabilityOfTenant = `SELECT EXISTS (SELECT 1 FROM limes.capabilities WHERE id = $2 AND tenant_id = $1)`

	// sqlClaimExchange records capability $4 for the key, unless the key
	// holds one: an insert racing it waits for it to commit, then inserts
	// nothing, and the next statement, by its own snapshot, reads the row
	// that won.
	sqlClaimExchange = `
INSERT INTO limes.exchanges (tenant_id, issuer, token_id, capability_id, token_expires_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, issuer, token_id) DO NOTHING
RETURNING capability_id`
)

// ExchangedFor implements limes.ExchangeStore, scoped to the key's tenant
// as every call naming a tenant is (Config.CheckScope).
func (s *Store) ExchangedFor(ctx context.Context, key limes.ExchangeKey) (uuid.UUID, error) {
	if err := key.Validate(); err != nil {
		return uuid.Nil(), err
	}
	var id uuid.UUID
	err := s.inTenantReadTx(ctx, key.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, sqlExchangedFor, key.TenantID, key.Issuer, key.TokenID).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil(), limes.ErrNotFound
	}
	return id, err
}

// ClaimExchange implements limes.ExchangeStore, scoped to the key's tenant
// as every call naming a tenant is (Config.CheckScope). Every transaction
// the store opens is READ COMMITTED, which is what lets a claim that lost
// a race read the winner's row.
func (s *Store) ClaimExchange(ctx context.Context, key limes.ExchangeKey, id uuid.UUID) (uuid.UUID, bool, error) {
	if err := key.ValidateClaim(); err != nil {
		return uuid.Nil(), false, err
	}
	var (
		recorded uuid.UUID
		claimed  bool
	)
	err := s.inTenantTx(ctx, key.TenantID, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, sqlCapabilityOfTenant, key.TenantID, id).Scan(&known); err != nil {
			return err
		}
		if !known {
			return limes.ErrNotFound
		}
		err := tx.QueryRow(ctx, sqlClaimExchange, key.TenantID, key.Issuer, key.TokenID, id, key.TokenExpiresAt).Scan(&recorded)
		if err == nil {
			claimed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, sqlExchangedFor, key.TenantID, key.Issuer, key.TokenID).Scan(&recorded)
	})
	if err != nil {
		return uuid.Nil(), false, err
	}
	return recorded, claimed, nil
}

// ScopedExchanges is s as the limes.ExchangeStore of an Exchanger behind a
// token endpoint, whose requests name no tenant: each call runs scoped to
// the tenant its key names, as WithTenant scopes it, as ScopedIssuance
// scopes the Issuer's Insert.
func ScopedExchanges(s *Store) limes.ExchangeStore { return scopedExchanges{s} }

// scopedExchanges is ScopedExchanges' store.
type scopedExchanges struct{ s *Store }

func (e scopedExchanges) ExchangedFor(ctx context.Context, key limes.ExchangeKey) (uuid.UUID, error) {
	return e.s.ExchangedFor(WithTenant(ctx, key.TenantID), key)
}

func (e scopedExchanges) ClaimExchange(ctx context.Context, key limes.ExchangeKey, id uuid.UUID) (uuid.UUID, bool, error) {
	return e.s.ClaimExchange(WithTenant(ctx, key.TenantID), key, id)
}

var (
	_ limes.ExchangeStore = (*Store)(nil)
	_ limes.ExchangeStore = scopedExchanges{}
)
