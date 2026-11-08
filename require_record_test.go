package limes_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

// recordTestTTL is the revocation cache's TTL in the RequireRecord tests,
// and recordTestSyncedTTL its bound while a signal listens: far longer, so
// a test tells which of the two an answer was held for.
const (
	recordTestTTL       = 2 * time.Second
	recordTestSyncedTTL = time.Hour
)

// recordFixture is an issuer that records into its own store, and a store
// that has none of its records: the lookup of a verifier blind to the
// issuer's records.
type recordFixture struct {
	keys    *jwks.Static
	issuer  *limes.Issuer
	records *memstore.Store
	blind   *memstore.Store
	token   string
	id      uuid.UUID
}

func newRecordFixture(t *testing.T) recordFixture {
	t.Helper()
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keys := exampleKeys(key.KeyID, key.Public())
	records := newMemstore(t)
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: exampleIssuerName})
	if err != nil {
		t.Fatal(err)
	}
	f := recordFixture{keys: keys, issuer: issuer, records: records, blind: newMemstore(t)}
	f.id, f.token = f.issue(t)
	return f
}

// issue mints a capability recorded in f.records.
func (f recordFixture) issue(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	res, err := f.issuer.Issue(context.Background(), limes.IssueRequest{
		ClientID: exampleClientID,
		IssuedBy: limes.Principal{Subject: "operator@example.com"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{exampleAudience},
		TTL:      time.Minute,
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet}, ResourcePrefixes: []string{"corpus/"},
			Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Capability.ID, res.Token
}

// verifier is a verifier over lookup, requiring a record or not.
func (f recordFixture) verifier(t *testing.T, lookup limes.RevocationLookup, require bool) *limes.Verifier {
	t.Helper()
	v, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: f.keys, Revocations: lookup, TrustedIssuers: []string{exampleIssuerName}, RequireRecord: require,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f recordFixture) verify(v *limes.Verifier, token string) error {
	_, err := v.Verify(context.Background(), limes.VerifyRequest{Token: token, Audience: exampleAudience})
	return err
}

func newMemstore(t *testing.T) *memstore.Store {
	t.Helper()
	s, err := memstore.NewStore(memstore.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// countingLineage is a lineage upstream that counts the questions it is
// asked.
type countingLineage struct {
	*memstore.Store
	asked atomic.Int64
}

func (c *countingLineage) RevocationLineage(ctx context.Context, id uuid.UUID) (limes.RevocationAnswer, error) {
	c.asked.Add(1)
	return c.Store.RevocationLineage(ctx, id)
}

// A capability the lookup has no record of verifies without RequireRecord —
// as before it — and is refused with it, as unrecorded and not as revoked,
// through the store itself and through a cache over it alike.
func TestRequireRecordRefusesACapabilityNotOnRecord(t *testing.T) {
	f := newRecordFixture(t)
	lookups := map[string]func() limes.RevocationLookup{
		"the store": func() limes.RevocationLookup { return f.blind },
		"a cache over it": func() limes.RevocationLookup {
			return newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: f.blind})
		},
	}
	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			if err := f.verify(f.verifier(t, lookup(), false), f.token); err != nil {
				t.Fatalf("without RequireRecord: %v, want accepted", err)
			}
			v := f.verifier(t, lookup(), true)
			err := f.verify(v, f.token)
			if !errors.Is(err, limes.ErrUnrecorded) || errors.Is(err, limes.ErrRevoked) || !errors.Is(err, limes.ErrUnauthenticated) {
				t.Fatalf("with RequireRecord: %v, want ErrUnrecorded, unauthenticated and not revoked", err)
			}
			if reason, ok := limes.ReasonOf(err); !ok || reason != limes.ReasonUnrecorded || reason.Permanent() {
				t.Errorf("ReasonOf = %q, %v; want %q, not permanent", reason, ok, limes.ReasonUnrecorded)
			}
			if code, ok := limes.CodeOf(err); !ok || code != limes.CodeUnrecorded {
				t.Errorf("CodeOf = %q, %v; want %q", code, ok, limes.CodeUnrecorded)
			}
			if resp := v.ErrorResponse(context.Background(), err, limes.AuthSchemeBearer); resp.Status != http.StatusUnauthorized {
				t.Errorf("ErrorResponse status = %d, want %d", resp.Status, http.StatusUnauthorized)
			}
		})
	}
}

// A capability on record answers as before under RequireRecord: accepted
// while live, refused as revoked once revoked, through a cache as well.
func TestRequireRecordKeepsTheAnswerOfARecord(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	cache := newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: f.records})
	for name, lookup := range map[string]limes.RevocationLookup{"the store": f.records, "a cache over it": cache} {
		if err := f.verify(f.verifier(t, lookup, true), f.token); err != nil {
			t.Fatalf("%s: a live capability on record: %v, want accepted", name, err)
		}
	}
	if _, err := f.records.Revoke(ctx, limes.RevokeRequest{CapabilityID: f.id, Note: "test"}); err != nil {
		t.Fatal(err)
	}
	cache.ClearRevoked(f.id)
	for name, lookup := range map[string]limes.RevocationLookup{"the store": f.records, "a cache over it": cache} {
		if err := f.verify(f.verifier(t, lookup, true), f.token); !errors.Is(err, limes.ErrRevoked) || errors.Is(err, limes.ErrUnrecorded) {
			t.Errorf("%s: a revoked capability: %v, want ErrRevoked alone", name, err)
		}
	}
}

// RequireRecord over a lookup that cannot tell a capability not on record
// from one not revoked is refused at construction, rather than not kept.
func TestRequireRecordRefusesALookupThatCannotTell(t *testing.T) {
	f := newRecordFixture(t)
	plain := isRevokedOnly{f.records}
	lookups := map[string]limes.RevocationLookup{
		"a lookup with IsRevoked alone": plain,
		"a cache over such a lookup":    newRevocationCache(t, limes.RevocationCacheConfig{Capabilities: plain}),
	}
	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			cfg := limes.VerifierConfig{Keys: f.keys, Revocations: lookup, TrustedIssuers: []string{exampleIssuerName}}
			if _, err := limes.NewVerifier(cfg); err != nil {
				t.Fatalf("without RequireRecord: %v", err)
			}
			cfg.RequireRecord = true
			if _, err := limes.NewVerifier(cfg); !errors.Is(err, limes.ErrInvalidConfig) {
				t.Errorf("with RequireRecord: %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// lookupOverCache is a type of the consumer's own wrapping a cache, which
// promotes the cache's RevocationLineage and so keeps the record check.
type lookupOverCache struct{ *limes.RevocationCache }

// A cache keeps the answer that a capability is not on record for its TTL —
// not for the SyncedTTL a listening signal allows, as no signal says when a
// record appears — and asks again once it has expired, or the cache is
// cleared, or the id is revoked. It is not revoked to IsRevoked throughout.
func TestRevocationCacheKeepsUnrecordedForItsTTL(t *testing.T) {
	ctx := context.Background()
	f := newRecordFixture(t)
	upstream := &countingLineage{Store: f.blind}
	now := time.Now()
	clock := func() time.Time { return now }
	cache := newRevocationCache(t, limes.RevocationCacheConfig{
		Capabilities: upstream, TTL: recordTestTTL, SyncedTTL: recordTestSyncedTTL, Now: clock,
	})
	signal := cache.NewSignal()
	defer signal.Close()
	signal.Listening(testSignalLease)
	v := f.verifier(t, cache, true)
	askedAfter := func(what string, want int64) {
		t.Helper()
		if got := upstream.asked.Load(); got != want {
			t.Errorf("%s: upstream asked %d times, want %d", what, got, want)
		}
	}

	for range 3 {
		if err := f.verify(v, f.token); !errors.Is(err, limes.ErrUnrecorded) {
			t.Fatalf("Verify: %v, want ErrUnrecorded", err)
		}
	}
	askedAfter("three verifications", 1)
	if st := cache.Stats(); st.Unrecorded.Hits != 2 || st.Unrecorded.Entries != 1 {
		t.Errorf("Stats().Unrecorded = %+v, want 2 hits and 1 entry", st.Unrecorded)
	}
	if revoked, err := cache.IsRevoked(ctx, f.id); err != nil || revoked {
		t.Errorf("IsRevoked of the unrecorded id = %v, %v; want not revoked", revoked, err)
	}
	askedAfter("IsRevoked from the cache", 1)

	now = now.Add(recordTestTTL)
	if err := f.verify(v, f.token); !errors.Is(err, limes.ErrUnrecorded) {
		t.Fatalf("Verify past the TTL: %v, want ErrUnrecorded", err)
	}
	askedAfter("a verification past the TTL, a signal listening", 2)

	cache.Clear()
	_ = f.verify(v, f.token)
	askedAfter("a verification after Clear", 3)

	cache.ClearRevoked(f.id)
	_ = f.verify(v, f.token)
	askedAfter("a verification after ClearRevoked of the id", 4)
}
