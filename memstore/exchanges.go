package memstore

import (
	"context"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
)

// ExchangedFor implements limes.ExchangeStore.
func (s *Store) ExchangedFor(_ context.Context, key limes.ExchangeKey) (uuid.UUID, error) {
	if err := key.Validate(); err != nil {
		return uuid.Nil(), err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.exchanges[exchangeKeyOf(key)]
	if !ok {
		return uuid.Nil(), limes.ErrNotFound
	}
	return e.id, nil
}

// ClaimExchange implements limes.ExchangeStore: under the store's lock, the
// first claim of a key is the one every later claim gets back.
func (s *Store) ClaimExchange(_ context.Context, key limes.ExchangeKey, id uuid.UUID) (uuid.UUID, bool, error) {
	if err := key.ValidateClaim(); err != nil {
		return uuid.Nil(), false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.caps[id]; !ok || c.Subject.TenantID != key.TenantID {
		return uuid.Nil(), false, limes.ErrNotFound
	}
	if recorded, ok := s.exchanges[exchangeKeyOf(key)]; ok {
		return recorded.id, false, nil
	}
	s.exchanges[exchangeKeyOf(key)] = exchange{id: id, tokenExpiresAt: key.TokenExpiresAt}
	return id, true, nil
}

// exchange is the capability a subject token's exchange issued, and when
// that token expires.
type exchange struct {
	id             uuid.UUID
	tokenExpiresAt time.Time
}

// exchangeKeyOf is key as the store holds it: without TokenExpiresAt,
// which ExchangedFor does not name.
func exchangeKeyOf(key limes.ExchangeKey) limes.ExchangeKey {
	return limes.ExchangeKey{TenantID: key.TenantID, Issuer: key.Issuer, TokenID: key.TokenID}
}

// forgetExchangesLocked drops the exchanges of the keys whose tokens
// expired before cutoff: none of them can be presented again. s.mu must
// be held.
func (s *Store) forgetExchangesLocked(of func(limes.ExchangeKey) bool, cutoff time.Time) {
	for k, e := range s.exchanges {
		if of(k) && e.tokenExpiresAt.Before(cutoff) {
			delete(s.exchanges, k)
		}
	}
}

var _ limes.ExchangeStore = (*Store)(nil)
