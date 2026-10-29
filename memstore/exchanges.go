package memstore

import (
	"context"
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
	id, ok := s.exchanges[key]
	if !ok {
		return uuid.Nil(), limes.ErrNotFound
	}
	return id, nil
}

// ClaimExchange implements limes.ExchangeStore: under the store's lock, the
// first claim of a key is the one every later claim gets back.
func (s *Store) ClaimExchange(_ context.Context, key limes.ExchangeKey, id uuid.UUID) (uuid.UUID, bool, error) {
	if err := key.Validate(); err != nil {
		return uuid.Nil(), false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.caps[id]; !ok || c.Subject.TenantID != key.TenantID {
		return uuid.Nil(), false, limes.ErrNotFound
	}
	if recorded, ok := s.exchanges[key]; ok {
		return recorded, false, nil
	}
	s.exchanges[key] = id
	s.exchangedBy[id] = key
	return id, true, nil
}

// forgetExchangeLocked drops the exchange that issued capID, as its record
// goes. s.mu must be held.
func (s *Store) forgetExchangeLocked(capID uuid.UUID) {
	if key, ok := s.exchangedBy[capID]; ok {
		delete(s.exchanges, key)
		delete(s.exchangedBy, capID)
	}
}

var _ limes.ExchangeStore = (*Store)(nil)
