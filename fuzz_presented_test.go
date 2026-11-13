package limes_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
)

// FuzzPresentedToken hands the verifier presented tokens the fuzzer writes
// around a bearer token the issuer signed, as a consumer's envelope carries
// it, and holds it to the one rule a presented token is read under: one
// past MaxTokenBytes and MaxEnvelopeBytes is ErrInvalidToken, one that does
// not hold the token is ErrInvalidRequest, and any other verifies as the
// token alone does — for a token bound to no key it changes nothing else.
func FuzzPresentedToken(f *testing.F) {
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
	res, err := issuer.Issue(ctx, limes.IssueRequest{
		ClientID: exampleClientID, IssuedBy: limes.Principal{Subject: "operator"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeService, TenantID: uuid.New(), Subject: "svc"},
		Audience: []string{fuzzOpAudience}, TTL: fuzzVerifyTTL,
		Caveats: limes.Caveats{Requests: limes.RequestCeiling{Unlimited: true}, Ops: []limes.Op{limes.OpGet}},
	})
	if err != nil {
		f.Fatal(err)
	}
	token := res.Token
	limit := limes.DefaultMaxTokenBytes + limes.MaxEnvelopeBytes
	for _, seed := range []string{"tenant." + token, token, token + ".sig", "tenant.", token[1:]} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, presented string) {
		if presented == "" {
			return // the token alone
		}
		got, err := verifier.Verify(ctx, limes.VerifyRequest{Token: token, Audience: fuzzOpAudience, PresentedToken: presented})
		switch {
		case len(presented) > limit:
			if !errors.Is(err, limes.ErrInvalidToken) {
				t.Fatalf("a presented token of %d bytes, past %d: %v, want ErrInvalidToken", len(presented), limit, err)
			}
		case !strings.Contains(presented, token):
			if !errors.Is(err, limes.ErrInvalidRequest) {
				t.Fatalf("a presented token without the token: %v, want ErrInvalidRequest", err)
			}
		case err != nil:
			t.Fatalf("a presented token holding the bearer token: %v", err)
		case got.Capability().ID != res.Capability.ID:
			t.Fatalf("verified as capability %s, issued as %s", got.Capability().ID, res.Capability.ID)
		}
	})
}
