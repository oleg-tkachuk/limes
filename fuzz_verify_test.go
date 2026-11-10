package limes_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// fuzzVerifyTTL is how long FuzzVerify's tokens live: past any run.
const fuzzVerifyTTL = 24 * time.Hour

// fuzzVerifyBudget is the budget ceiling of one of FuzzVerify's tokens, so
// a token with an amount in its claims is among the seeds.
const fuzzVerifyBudget = 5 * limes.NanosPerUnit

// FuzzVerify hands the verifier whole tokens the fuzzer writes, mutated
// from ones the issuer signed, and holds it to the one answer a forger must
// never get: what it accepts is a token the issuer signed, byte for byte —
// no other spelling of its header, claims or signature verifies — and
// carries the capability the issuer gave it.
func FuzzVerify(f *testing.F) {
	key, err := limes.GenerateKey()
	if err != nil {
		f.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		f.Fatal(err)
	}
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: discardStore{}, IssuerName: fuzzOpIssuer})
	if err != nil {
		f.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		f.Fatal(err)
	}
	verifier, err := limes.NewVerifier(limes.VerifierConfig{Keys: keys, Revocations: discardStore{}, AllowUnrecorded: true, TrustedIssuers: []string{fuzzOpIssuer}})
	if err != nil {
		f.Fatal(err)
	}
	ctx := context.Background()
	subject := limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"}
	issued := map[string]uuid.UUID{}
	for _, caveats := range []limes.Caveats{
		{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}},
		{
			Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet, limes.OpPut},
			Budget:           limes.BudgetCeiling{Max: fuzzVerifyBudget, UnitCode: "USD"},
			ResourcePrefixes: []string{"docs/"},
		},
	} {
		res, err := issuer.Issue(ctx, limes.IssueRequest{
			ClientID: exampleClientID, IssuedBy: limes.Principal{Subject: "operator"}, Subject: subject,
			Audience: []string{fuzzOpAudience}, TTL: fuzzVerifyTTL, Caveats: caveats,
		})
		if err != nil {
			f.Fatal(err)
		}
		issued[res.Token] = res.Capability.ID
		f.Add(res.Token)
	}
	f.Fuzz(func(t *testing.T, token string) {
		got, err := verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: fuzzOpAudience})
		if err != nil {
			return
		}
		id, ok := issued[token]
		if !ok {
			t.Fatalf("verified a token the issuer never signed: %q", token)
		}
		if got.Capability().ID != id {
			t.Fatalf("token %q verified as capability %s, issued as %s", token, got.Capability().ID, id)
		}
	})
}
