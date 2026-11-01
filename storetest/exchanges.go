package storetest

import (
	"errors"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/internal/expect"
)

// concurrentClaims is how many claims of one key race in the exchange
// check, each with a capability of its own.
const concurrentClaims = 8

// exchangeIssuer is the subject tokens' issuer the exchange checks key on.
const exchangeIssuer = "https://idp.example"

// exchangeTokenLife is how long past f.now the subject tokens of the
// exchange checks live: past every capability they are exchanged for.
const exchangeTokenLife = 24 * time.Hour

// exchangeKey is a key of f's tenant for the subject token jti, which
// expires exchangeTokenLife from now.
func (f fixture) exchangeKey(jti string) limes.ExchangeKey {
	return limes.ExchangeKey{TenantID: f.Tenant, Issuer: exchangeIssuer, TokenID: jti, TokenExpiresAt: f.now.Add(exchangeTokenLife)}
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
	foreign := f.exchangeKey("jti-foreign")
	foreign.TenantID = other
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
	noExpiry := f.exchangeKey("jti-no-expiry")
	noExpiry.TokenExpiresAt = time.Time{}
	_, _, err = f.Exchanges.ClaimExchange(f.Ctx, noExpiry, live.ID)
	expect.That(t, errors.Is(err, limes.ErrInvalidRequest), "a claim naming no token expiry: %v, want ErrInvalidRequest", err)
}

// The record outlives its capability: Purge keeps it while its subject
// token can still be presented, so that token is never exchanged afresh,
// and takes it once the token has expired past the retention.
func checkExchangePurge(t *testing.T, f fixture) {
	f.requireExchanges(t)
	held := f.insert(t, f.newCapability("agent:old", uuid.Nil(), f.now.Add(-expiredAgo)))
	spent := f.insert(t, f.newCapability("agent:older", uuid.Nil(), f.now.Add(-expiredAgo)))
	kept, gone := f.exchangeKey("jti-presentable"), f.exchangeKey("jti-expired")
	gone.TokenExpiresAt = f.now.Add(-expiredAgo)
	for key, id := range map[limes.ExchangeKey]uuid.UUID{kept: held.ID, gone: spent.ID} {
		_, claimed, err := f.Exchanges.ClaimExchange(f.Ctx, key, id)
		expect.Must(t, err == nil && claimed, "claim %s: %v, %v", key.TokenID, claimed, err)
	}
	purged, err := f.Store.Purge(f.Ctx, limes.PurgeRequest{ExpiredFor: purgeGrace})
	expect.Must(t, err == nil && purged.Purged >= 2, "Purge = %+v, %v; want both capabilities purged", purged, err)
	got, err := f.Exchanges.ExchangedFor(f.Ctx, kept)
	expect.That(t, err == nil && got == held.ID, "the exchange of a token still presentable = %s, %v; want %s kept past its capability", got, err, held.ID)
	again := f.live(t, "agent:again", uuid.Nil())
	got, claimed, err := f.Exchanges.ClaimExchange(f.Ctx, kept, again.ID)
	expect.That(t, err == nil && !claimed && got == held.ID, "a claim of that token again = %s, %v, %v; want %s, not claimed", got, claimed, err, held.ID)
	_, err = f.Exchanges.ExchangedFor(f.Ctx, gone)
	expect.That(t, errors.Is(err, limes.ErrNotFound), "the exchange of an expired token: %v, want ErrNotFound", err)
}
