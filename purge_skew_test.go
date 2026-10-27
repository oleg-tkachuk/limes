package limes_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes"
	"github.com/oleg-tkachuk/limes/jwks"
	"github.com/oleg-tkachuk/limes/memstore"
)

// A revoked token stays refused while any verifier still accepts it, even by
// a clock behind the purging store's: the store's Purge keeps the revocation
// for MaxLeeway past expiry plus PurgeClockSkew.
func TestPurgeKeepsARevocationAcrossClockSkew(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0)
	verifierNow, storeNow := start, start
	records, err := memstore.NewStore(memstore.StoreConfig{Now: func() time.Time { return storeNow }})
	if err != nil {
		t.Fatal(err)
	}
	key, err := limes.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := limes.NewSigner(limes.SignerConfig{KeyID: key.KeyID, Key: key.Private})
	if err != nil {
		t.Fatal(err)
	}
	const issuerName = "skew-issuer"
	issuer, err := limes.NewIssuer(limes.IssuerConfig{Signer: signer, Store: records, IssuerName: issuerName, Now: func() time.Time { return start }})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{key.KeyID: key.Public()}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := limes.NewVerifier(limes.VerifierConfig{
		Keys: keys, Revocations: records,
		TrustedIssuers: []string{issuerName}, Leeway: limes.MaxLeeway, Now: func() time.Time { return verifierNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	cResult, err := issuer.Issue(ctx, limes.IssueRequest{
		IssuedBy: limes.Principal{Subject: "op"},
		Subject:  limes.Principal{Type: limes.PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{exampleAudience}, Caveats: limes.Caveats{Ops: []limes.Op{limes.OpGet}}, TTL: time.Minute,
	})
	c, token := cResult.Capability, cResult.Token
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.Revoke(ctx, limes.RevokeRequest{CapabilityID: c.ID}); err != nil {
		t.Fatal(err)
	}
	// The verifier is at the last instant it accepts the token; the store's
	// clock runs ahead by just under the allowance.
	verifierNow = c.ExpiresAt.Add(limes.MaxLeeway - time.Nanosecond)
	storeNow = verifierNow.Add(limes.PurgeClockSkew - time.Second)
	if _, err := records.Purge(ctx, limes.PurgeRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, limes.VerifyRequest{Token: token, Audience: exampleAudience}); !errors.Is(err, limes.ErrRevoked) {
		t.Errorf("Verify after a purge by a clock %s ahead: err = %v, want ErrRevoked", limes.PurgeClockSkew-time.Second, err)
	}
}
