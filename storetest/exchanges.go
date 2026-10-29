package storetest

import (
	"errors"
	"sync"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
)

// concurrentClaims is how many claims of one key race in the exchange
// check, each with a capability of its own.
const concurrentClaims = 8

// exchangeIssuer is the subject tokens' issuer the exchange checks key on.
const exchangeIssuer = "https://idp.example"

// exchangeKey is a key of f's tenant for the subject token jti.
func (f fixture) exchangeKey(jti string) limes.ExchangeKey {
	return limes.ExchangeKey{TenantID: f.Tenant, Issuer: exchangeIssuer, TokenID: jti}
}

// requireExchanges skips a check of limes.ExchangeStore without one.
func (f fixture) requireExchanges(t *testing.T) {
	t.Helper()
	if f.Exchanges == nil {
		t.Skip("Env.Exchanges not set")
	}
}

// The first claim of a key records its capability; every later one, and
// every one racing it, gets that capability back and records nothing.
func checkExchangeClaims(t *testing.T, f fixture) {
	f.requireExchanges(t)
	key := f.exchangeKey("jti-sequential")
	_, err := f.Exchanges.ExchangedFor(f.Ctx, key)
	expect.Must(t, errors.Is(err, limes.ErrNotFound), "ExchangedFor of a key never claimed: %v, want ErrNotFound", err)
	first, second := f.live(t, "agent:first", uuid.Nil()), f.live(t, "agent:second", uuid.Nil())
	got, claimed, err := f.Exchanges.ClaimExchange(f.Ctx, key, first.ID)
	expect.Must(t, err == nil && claimed && got == first.ID, "first claim = %s, %v, %v; want %s claimed", got, claimed, err, first.ID)
	got, claimed, err = f.Exchanges.ClaimExchange(f.Ctx, key, second.ID)
	expect.Must(t, err == nil && !claimed && got == first.ID, "second claim = %s, %v, %v; want %s, not claimed", got, claimed, err, first.ID)
	got, err = f.Exchanges.ExchangedFor(f.Ctx, key)
	expect.Must(t, err == nil && got == first.ID, "ExchangedFor = %s, %v; want %s", got, err, first.ID)
	other, err := f.Exchanges.ExchangedFor(f.Ctx, f.exchangeKey("jti-other"))
	expect.That(t, errors.Is(err, limes.ErrNotFound), "ExchangedFor of another token = %s, %v; want ErrNotFound", other, err)

	raced := f.exchangeKey("jti-raced")
	ids := make([]uuid.UUID, concurrentClaims)
	for i := range ids {
		ids[i] = f.live(t, "agent:racer", uuid.Nil()).ID
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = map[uuid.UUID]int{}
		winners int
	)
	for _, id := range ids {
		wg.Go(func() {
			got, claimed, err := f.Exchanges.ClaimExchange(f.Ctx, raced, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("a racing claim: %v", err)
				return
			}
			results[got]++
			if claimed {
				winners++
			}
		})
	}
	wg.Wait()
	expect.That(t, len(results) == 1 && winners == 1, "racing claims returned %v with %d claimed; want one capability, claimed once", results, winners)
}

// A claim names a capability on record in the key's tenant, and a key
// names a tenant, an issuer and a token.
func checkExchangeRefusals(t *testing.T, f fixture) {
	f.requireExchanges(t)
	live := f.live(t, "agent:claimed", uuid.Nil())
	_, _, err := f.Exchanges.ClaimExchange(f.Ctx, f.exchangeKey("jti-unknown"), uuid.New())
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a claim of an id not on record: %v, want ErrNotFound", err)
	other, err := f.newTenant()
	expect.Must(t, err == nil, "NewTenant: %v", err)
	foreign := limes.ExchangeKey{TenantID: other, Issuer: exchangeIssuer, TokenID: "jti-foreign"}
	_, _, err = f.Exchanges.ClaimExchange(f.tenantCtx(other), foreign, live.ID)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "a claim in another tenant than the capability's: %v, want ErrNotFound", err)
	for name, key := range map[string]limes.ExchangeKey{
		"no tenant":   {Issuer: exchangeIssuer, TokenID: "jti"},
		"no issuer":   {TenantID: f.Tenant, TokenID: "jti"},
		"no token id": {TenantID: f.Tenant, Issuer: exchangeIssuer},
	} {
		_, _, err := f.Exchanges.ClaimExchange(f.Ctx, key, live.ID)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: ClaimExchange = %v, want ErrInvalidRequest", name, err)
		_, err = f.Exchanges.ExchangedFor(f.Ctx, key)
		expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "%s: ExchangedFor = %v, want ErrInvalidRequest", name, err)
	}
}

// The mapping goes when Purge takes its capability: a key then holds
// nothing, and the next exchange of its token issues afresh.
func checkExchangePurge(t *testing.T, f fixture) {
	f.requireExchanges(t)
	expired := f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-expiredAgo)))
	live := f.live(t, "agent:new", uuid.Nil())
	gone, kept := f.exchangeKey("jti-expired"), f.exchangeKey("jti-live")
	for key, id := range map[limes.ExchangeKey]uuid.UUID{gone: expired.ID, kept: live.ID} {
		_, claimed, err := f.Exchanges.ClaimExchange(f.Ctx, key, id)
		expect.Must(t, err == nil && claimed, "claim %s: %v, %v", key.TokenID, claimed, err)
	}
	_, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	expect.Must(t, err == nil, "Purge: %v", err)
	_, err = f.Exchanges.ExchangedFor(f.Ctx, gone)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "the exchange of a purged capability: %v, want ErrNotFound", err)
	got, err := f.Exchanges.ExchangedFor(f.Ctx, kept)
	expect.That(t, err == nil && got == live.ID, "the exchange of a live capability = %s, %v; want %s", got, err, live.ID)
}
