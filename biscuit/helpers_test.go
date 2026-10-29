package biscuit

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/dpop"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

// The golden inputs the shared fixtures were minted from, as the limes
// module's own golden tests fix them.
var goldenSeed = [32]byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

const (
	goldenKID    = jwks.KeyIDPrefix + "golden"
	goldenIssuer = "limes-golden"
	// The DPoP request the tests' proofs sign.
	dpopURL    = "https://data.example.com/example.data.v1.ObjectService/GetObject"
	dpopMethod = "POST"
)

// goldenClock is the fixtures' fixed issuance instant.
func goldenClock() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// revLookup revokes nothing, for verifiers over the fixtures.
type revLookup struct{}

func (revLookup) IsRevoked(context.Context, uuid.UUID) (bool, error) { return false, nil }

func (revLookup) IsBiscuitRevoked(context.Context, [][]byte) (bool, error) { return false, nil }

// newReplay is dpop.NewMemoryReplayCache, failing the test on a refusal.
func newReplay(t testing.TB, cfg dpop.MemoryReplayCacheConfig) *dpop.MemoryReplayCache {
	t.Helper()
	c, err := dpop.NewMemoryReplayCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newVerifier is dpop.NewVerifier, failing the test on a refusal.
func newVerifier(t testing.TB, cfg dpop.VerifierConfig) *dpop.Verifier {
	t.Helper()
	v, err := dpop.NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// staticKeys is a jwks.Static over keys, failing the test on a refusal.
func staticKeys(t testing.TB, keys map[string]ed25519.PublicKey) *jwks.Static {
	t.Helper()
	s, err := jwks.NewStatic(jwks.StaticConfig{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// withProof is a request presenting token with a DPoP proof key signed at
// now.
func withProof(ctx context.Context, t *testing.T, key crypto.Signer, token, audience string, now time.Time) limes.VerifyRequest {
	t.Helper()
	proof, err := dpop.Sign(ctx, key, dpop.SignRequest{Method: dpopMethod, URL: dpopURL, Token: token, IssuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return limes.VerifyRequest{Token: token, Audience: audience, DPoP: dpop.Proof{JWT: proof, Method: dpopMethod, URL: dpopURL}}
}

// The capability the fixtures issue, and whom to.
const (
	testClientID  = "client-1"
	audienceData  = "data"
	audienceMCP   = "mcp"
	goldenTenant  = "11111111-1111-1111-1111-111111111111"
	goldenSubject = "golden-agent"
	testIssuer    = "limes-test"
	testTTL       = time.Hour
)

// stack is an issuer and a verifier sharing a memstore and a key, with the
// Biscuit form on both.
type stack struct {
	issuer   *limes.Issuer
	verifier *limes.Verifier
	records  *memstore.Store
}

// newStack is a stack over priv, signing as kid for issuerName, on the
// clock now.
func newStack(t testing.TB, priv ed25519.PrivateKey, kid, issuerName string, now func() time.Time, lookup limes.BiscuitRevocationLookup) stack {
	t.Helper()
	records, err := memstore.NewStore(memstore.StoreConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: limes.SealKey(priv)})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName, Now: now, Biscuit: Form()})
	if err != nil {
		t.Fatal(err)
	}
	if lookup == nil {
		lookup = records
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{kid: priv.Public().(ed25519.PublicKey)}),
		Revocations:    records,
		TrustedIssuers: []string{issuerName},
		Now:            now,
		Biscuit:        &limes.BiscuitConfig{Revocations: lookup, Form: Form(), MeterCopies: true},
		DPoP:           newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{}), Now: now}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return stack{issuer: issuer, verifier: verifier, records: records}
}

// seal issues req and returns the capability and its Biscuit form, as the
// verifier accepts the issued token.
func (s stack) seal(t testing.TB, req limes.IssueRequest) (limes.Capability, string) {
	t.Helper()
	ctx := context.Background()
	issued, err := s.issuer.Issue(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := s.verifier.Verify(ctx, limes.VerifyRequest{Token: issued.Token, Audience: req.Audience[0]})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: vc})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Capability, res.Token
}

// biscuitFixture is a stack over a fresh key and the Biscuit form of a
// capability of an agent of a fresh tenant, to get, list, put and share
// under corpus/ on both planes.
func biscuitFixture(t testing.TB) (stack, limes.Capability, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newStack(t, priv, jwks.KeyIDPrefix+"test", testIssuer, time.Now, nil)
	cap, token := s.seal(t, limes.IssueRequest{ClientID: testClientID,
		IssuedBy: limes.Principal{Subject: "op"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData, audienceMCP},
		Caveats: limes.Caveats{
			Ops: []limes.Op{limes.OpGet, limes.OpList, limes.OpPut, limes.OpShare}, ResourcePrefixes: []string{"corpus/"},
			Requests: limes.RequestCeiling{Unlimited: true}, Budget: limes.BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		},
		TTL: testTTL,
	})
	return s, cap, token
}

// goldenStack is the stack the shared fixtures were minted and are verified
// with: the golden key and clock, revoking what lookup says, or nothing.
func goldenStack(t testing.TB, lookup limes.BiscuitRevocationLookup) stack {
	t.Helper()
	if lookup == nil {
		lookup = revLookup{}
	}
	return newStack(t, ed25519.NewKeyFromSeed(goldenSeed[:]), goldenKID, goldenIssuer, goldenClock, lookup)
}

// ceilingIs reports whether got is set, to want.
func ceilingIs[T comparable](got *T, want T) bool { return got != nil && *got == want }
