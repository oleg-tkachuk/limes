package limes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	mathrand "math/rand/v2"
	"testing"
	"time"

	biscuit "github.com/eclipse-biscuit/biscuit-go/v2"
	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

func biscuitFixture(t *testing.T) (*Issuer, *Verifier, *memStore, Capability, string) {
	t.Helper()
	issuer, _, store, pub := buildIssuerVerifier(t)
	verifier, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    store,
		TrustedIssuers: []string{"limes-test"},
		AcceptBiscuit:  true,
		DPoP:           newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{})}),

		BiscuitRevocations: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	cap, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{audienceData, audienceMCP},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, OpPut},
			ResourcePrefixes: []string{"corpus/"},
			Requests:         RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Unlimited: true, UnitCode: "USD"},
		},
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.biscuit(context.Background(), asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	return issuer, verifier, store, cap, token
}

func TestBiscuitVerifiesAsItsCapability(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	if !isBiscuit(token) {
		t.Fatal("Biscuit form not recognised")
	}
	got, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Capability().ID != cap.ID || len(got.Capability().Caveats.Ops) != 3 || got.Capability().BiscuitRoot != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestBiscuitAttenuationNarrowsOffline(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	soon := time.Now().Add(10 * time.Minute)
	narrowed, err := Attenuate(token, Attenuation{
		Ops:              []Op{OpGet},
		ResourcePrefixes: []string{"corpus/public/"},
		Audience:         []string{audienceData},
		ExpiresAt:        soon,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), VerifyRequest{Token: narrowed, Audience: audienceData})
	if err != nil {
		t.Fatalf("verify narrowed: %v", err)
	}
	if got.Capability().ID != cap.ID {
		t.Errorf("narrowed token is a different capability")
	}
	if len(got.Capability().Caveats.Ops) != 1 || got.Capability().Caveats.Ops[0] != OpGet {
		t.Errorf("ops = %v", got.Capability().Caveats.Ops)
	}
	if err := got.Capability().Caveats.Check(CheckRequest{Op: OpGet, Resource: "corpus/private/x"}); err == nil {
		t.Error("narrowed token reached outside its prefix")
	}
	if err := got.Capability().Caveats.Check(CheckRequest{Op: OpGet, Resource: "corpus/public/x"}); err != nil {
		t.Errorf("narrowed token refused inside its prefix: %v", err)
	}
	if !got.Capability().ExpiresAt.Equal(soon.UTC().Truncate(time.Second)) {
		t.Errorf("expires = %v, want %v", got.Capability().ExpiresAt, soon)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: narrowed, Audience: audienceMCP}); !errors.Is(err, ErrAudienceMismatch) {
		t.Errorf("dropped plane: err = %v, want ErrAudienceMismatch", err)
	}

	// A second block narrows further; the original still works as before.
	again, err := Attenuate(narrowed, Attenuation{ResourceURIs: []string{"corpus/public/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Verify(context.Background(), VerifyRequest{Token: again, Audience: audienceData}); err != nil || len(got.Capability().Caveats.ResourceURIs) != 1 {
		t.Fatalf("two blocks: %+v, %v", got, err)
	}
	if got, err := v.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceMCP}); err != nil || len(got.Capability().Caveats.Ops) != 3 {
		t.Fatalf("original after attenuation: %v", err)
	}
}

func TestBiscuitAttenuationExpiryIsEnforced(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	past, err := Attenuate(token, Attenuation{ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: past, Audience: audienceData}); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestBiscuitAttenuationCannotWiden(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	cases := map[string]Attenuation{
		"an op the token lacks":   {Ops: []Op{OpDelete}},
		"a resource outside":      {ResourcePrefixes: []string{"other/"}},
		"a plane the token lacks": {Audience: []string{audienceAdmin}},
		"a later expiry":          {ExpiresAt: time.Now().Add(48 * time.Hour)},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			wide, err := Attenuate(token, a)
			if err != nil {
				t.Fatal(err)
			}
			got, err := v.Verify(context.Background(), VerifyRequest{Token: wide, Audience: audienceData})
			if name == "a later expiry" {
				// A later expiry is not a widening the token can express: the
				// earlier one stands.
				if err != nil || got.Capability().ExpiresAt.After(time.Now().Add(2*time.Hour)) {
					t.Fatalf("later expiry: %v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrBiscuitAttenuation) || !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrBiscuitAttenuation", err)
			}
		})
	}
}

func TestBiscuitRefusesWhatItCannotEnforce(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	b, err := parseBiscuit(token)
	if err != nil {
		t.Fatal(err)
	}
	blk := b.CreateBlock()
	if err := blk.AddCheck(biscuit.Check{Queries: []biscuit.Rule{{
		Head: biscuit.Predicate{Name: "q", IDs: []biscuit.Term{biscuit.Variable("x")}},
		Body: []biscuit.Predicate{{Name: "operation", IDs: []biscuit.Term{biscuit.Variable("x")}}},
	}}}); err != nil {
		t.Fatal(err)
	}
	withCheck, err := b.Append(rand.Reader, blk.Build())
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := serializeBiscuit(withCheck)
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrBiscuitAttenuation) {
		t.Fatalf("datalog check: err = %v, want ErrBiscuitAttenuation", err)
	}

	b, _ = parseBiscuit(token)
	blk = b.CreateBlock()
	_ = blk.AddFact(biscuitFact("right", biscuit.String("everything")))
	withFact, _ := b.Append(rand.Reader, blk.Build())
	tok, _ = serializeBiscuit(withFact)
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrBiscuitAttenuation) {
		t.Fatalf("foreign fact: err = %v, want ErrBiscuitAttenuation", err)
	}
}

func TestBiscuitCannotBeStrippedOrForged(t *testing.T) {
	issuer, v, _, cap, token := biscuitFixture(t)
	narrowed, _ := Attenuate(token, Attenuation{Ops: []Op{OpGet}})

	// The sealed JWT, lifted out of the attenuated Biscuit and presented
	// alone, would carry the full parent — it is refused.
	inner, _, err := openBiscuit(narrowed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: inner, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("sealed token alone: err = %v, want ErrInvalidToken", err)
	}

	// Re-wrapping that JWT in a Biscuit of one's own fails: its root is not
	// the one the JWT vouches for.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	bb := biscuit.NewBuilder(priv)
	_ = bb.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(inner)))
	forged, _ := bb.Build()
	tok, _ := serializeBiscuit(forged)
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("re-wrapped: err = %v, want ErrInvalidToken", err)
	}

	// A Biscuit around an ordinary token (no root claim) is refused too.
	_, plain, _ := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: cap.Subject,
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	bb = biscuit.NewBuilder(priv)
	_ = bb.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(plain)))
	wrapped, _ := bb.Build()
	tok, _ = serializeBiscuit(wrapped)
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrapped plain token: err = %v, want ErrInvalidToken", err)
	}

	// Tampering with any byte breaks the chain.
	raw, _ := base64.RawURLEncoding.DecodeString(narrowed)
	raw[len(raw)-3] ^= 1
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: base64.RawURLEncoding.EncodeToString(raw), Audience: audienceData}); err == nil {
		t.Fatal("tampered Biscuit accepted")
	}
}

func TestBiscuitRevokedWithItsCapability(t *testing.T) {
	_, v, store, cap, token := biscuitFixture(t)
	narrowed, _ := Attenuate(token, Attenuation{Ops: []Op{OpGet}})
	if err := store.Revoke(context.Background(), RevokeRequest{CapabilityID: cap.ID, Note: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: narrowed, Audience: audienceData}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}

func TestBiscuitOfflineBinding(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	keys := dpopKeys(t)
	jkt, _ := dpop.Thumbprint(keys["Ed25519"].Public())
	bound, err := Attenuate(token, Attenuation{ConfirmationJKT: jkt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: bound, Audience: audienceData}); !errors.Is(err, dpop.ErrRequired) {
		t.Fatalf("bound offline, no proof: err = %v, want dpop.ErrRequired", err)
	}
	got, err := v.Verify(context.Background(), withProof(t.Context(), t, keys["Ed25519"], bound, audienceData, time.Now()))
	if err != nil || got.Capability().ConfirmationJKT != jkt {
		t.Fatalf("bound offline: %q, %v", got.Capability().ConfirmationJKT, err)
	}
	other, _ := dpop.Thumbprint(keys["P-256"].Public())
	rebound, _ := Attenuate(bound, Attenuation{ConfirmationJKT: other})
	if _, err := v.Verify(context.Background(), VerifyRequest{Token: rebound, Audience: audienceData}); !errors.Is(err, ErrBiscuitAttenuation) {
		t.Fatalf("rebinding offline: err = %v, want ErrBiscuitAttenuation", err)
	}
}

func TestVerifierRefusesBiscuitUnlessEnabled(t *testing.T) {
	_, plainVerifier, _, _ := buildIssuerVerifier(t)
	_, _, _, _, token := biscuitFixture(t)
	if _, err := plainVerifier.Verify(context.Background(), VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v, want ErrInvalidToken", err)
	}
}

// Whatever chain of blocks a holder appends, the verified result is either
// refused or within the original: the delegation fuzz property, over chains.
func TestBiscuitAttenuationChainsNeverWiden(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	r := mathrand.New(mathrand.NewPCG(7, 11))
	ops := []Op{OpGet, OpList, OpPut, OpDelete, OpShare}
	prefixes := []string{"corpus/", "corpus/public/", "corpus/public/a", "other/", "corpus-x/", "corpus/private/"}
	planes := []string{audienceData, audienceMCP, audienceAdmin}
	pick := func(from []string) []string {
		var out []string
		for _, s := range from {
			if r.IntN(3) == 0 {
				out = append(out, s)
			}
		}
		return out
	}
	verified := 0
	for range 300 {
		tok := token
		for range 1 + r.IntN(3) {
			var a Attenuation
			if r.IntN(2) == 0 {
				for _, op := range ops {
					if r.IntN(3) == 0 {
						a.Ops = append(a.Ops, op)
					}
				}
			}
			if r.IntN(2) == 0 {
				a.ResourcePrefixes = pick(prefixes)
			}
			if r.IntN(3) == 0 {
				a.Audience = pick(planes)
			}
			if r.IntN(3) == 0 {
				a.ExpiresAt = time.Now().Add(time.Duration(r.IntN(240)-60) * time.Minute)
			}
			next, err := Attenuate(tok, a)
			if err != nil {
				t.Fatal(err)
			}
			tok = next
		}
		for _, plane := range planes {
			got, err := v.Verify(context.Background(), VerifyRequest{Token: tok, Audience: plane})
			if err != nil {
				continue
			}
			verified++
			if err := Narrows(cap, got.Capability()); err != nil {
				t.Fatalf("a verified chain widened its capability: %v\n%+v", err, got.Capability().Caveats)
			}
		}
	}
	if verified == 0 {
		t.Fatal("no chain verified; the property was never exercised")
	}
}

// Biscuit signs only what this issuer issued, as the verifier read it back:
// never a value a caller assembled, a Biscuit copy wrapped again, another
// issuer's capability, or one that has expired or been revoked.
func TestBiscuitTakesOnlyAVerifiedCapabilityOfItsOwn(t *testing.T) {
	issuer, v, store, cap, token := biscuitFixture(t)
	ctx := context.Background()
	jwt, err := issuer.signer.sign(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := v.Verify(ctx, VerifyRequest{Token: jwt, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.biscuit(ctx, vc); err != nil {
		t.Fatalf("Biscuit of a verified capability: %v", err)
	}
	copyOf, err := v.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData})
	if err != nil {
		t.Fatal(err)
	}
	foreign := cap
	foreign.Issuer = foreignIssuer
	expired := cap
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	for name, tc := range map[string]struct {
		vc   VerifiedCapability
		want error
	}{
		"the zero value":        {VerifiedCapability{}, ErrInvalidRequest},
		"a Biscuit copy":        {copyOf, ErrInvalidRequest},
		"another issuer's":      {asVerified(foreign), ErrInvalidRequest},
		"an expired capability": {asVerified(expired), ErrExpired},
	} {
		if _, err := issuer.biscuit(ctx, tc.vc); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if err := store.Revoke(ctx, RevokeRequest{CapabilityID: cap.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.biscuit(ctx, vc); !errors.Is(err, ErrRevoked) {
		t.Errorf("a revoked capability: err = %v, want ErrRevoked", err)
	}
}
