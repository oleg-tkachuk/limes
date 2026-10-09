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

	biscuit "github.com/biscuit-auth/biscuit-go/v2"
	"github.com/google/uuid"
)

func biscuitFixture(t *testing.T) (*Issuer, *StandardVerifier, *memStore, Capability, string) {
	t.Helper()
	issuer, _, store, pub := buildIssuerVerifier(t)
	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub}),
		Revocations:    NewCachedRevocationChecker(store, -1),
		TrustedIssuers: []string{"limes-test"},
		AcceptBiscuit:  true,

		BiscuitRevocations: NewCachedBiscuitRevocationChecker(store, -1),
	})
	if err != nil {
		t.Fatal(err)
	}
	cap, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"},
		Subject:  Principal{TenantID: uuid.New(), Subject: "agent"},
		Audience: []string{AudiencePlaneData, AudiencePlaneMCP},
		Caveats: Caveats{
			Ops:              []Op{OpGet, OpList, OpPut},
			ResourcePrefixes: []string{"corpus/"},
		},
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Biscuit(cap)
	if err != nil {
		t.Fatal(err)
	}
	return issuer, verifier, store, cap, token
}

func TestBiscuitVerifiesAsItsCapability(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	if !IsBiscuit(token) {
		t.Fatal("Biscuit form not recognised")
	}
	got, err := v.Verify(context.Background(), token, AudiencePlaneData)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ID != cap.ID || len(got.Caveats.Ops) != 3 || got.BiscuitRoot != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestBiscuitAttenuationNarrowsOffline(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	soon := time.Now().Add(10 * time.Minute)
	narrowed, err := Attenuate(token, Attenuation{
		Ops:              []Op{OpGet},
		ResourcePrefixes: []string{"corpus/public/"},
		Planes:           []string{AudiencePlaneData},
		ExpiresAt:        soon,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), narrowed, AudiencePlaneData)
	if err != nil {
		t.Fatalf("verify narrowed: %v", err)
	}
	if got.ID != cap.ID {
		t.Errorf("narrowed token is a different capability")
	}
	if len(got.Caveats.Ops) != 1 || got.Caveats.Ops[0] != OpGet {
		t.Errorf("ops = %v", got.Caveats.Ops)
	}
	if err := got.Caveats.Check(CheckRequest{Op: OpGet, Resource: "corpus/private/x"}); err == nil {
		t.Error("narrowed token reached outside its prefix")
	}
	if err := got.Caveats.Check(CheckRequest{Op: OpGet, Resource: "corpus/public/x"}); err != nil {
		t.Errorf("narrowed token refused inside its prefix: %v", err)
	}
	if !got.ExpiresAt.Equal(soon.UTC().Truncate(time.Second)) {
		t.Errorf("expires = %v, want %v", got.ExpiresAt, soon)
	}
	if _, err := v.Verify(context.Background(), narrowed, AudiencePlaneMCP); !errors.Is(err, ErrAudienceMismatch) {
		t.Errorf("dropped plane: err = %v, want ErrAudienceMismatch", err)
	}

	// A second block narrows further; the original still works as before.
	again, err := Attenuate(narrowed, Attenuation{ResourceURIs: []string{"corpus/public/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Verify(context.Background(), again, AudiencePlaneData); err != nil || len(got.Caveats.ResourceURIs) != 1 {
		t.Fatalf("two blocks: %+v, %v", got, err)
	}
	if got, err := v.Verify(context.Background(), token, AudiencePlaneMCP); err != nil || len(got.Caveats.Ops) != 3 {
		t.Fatalf("original after attenuation: %v", err)
	}
}

func TestBiscuitAttenuationExpiryIsEnforced(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	past, err := Attenuate(token, Attenuation{ExpiresAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), past, AudiencePlaneData); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestBiscuitAttenuationCannotWiden(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	cases := map[string]Attenuation{
		"an op the token lacks":   {Ops: []Op{OpDelete}},
		"a resource outside":      {ResourcePrefixes: []string{"other/"}},
		"a plane the token lacks": {Planes: []string{AudiencePlaneAdmin}},
		"a later expiry":          {ExpiresAt: time.Now().Add(48 * time.Hour)},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			wide, err := Attenuate(token, a)
			if err != nil {
				t.Fatal(err)
			}
			got, err := v.Verify(context.Background(), wide, AudiencePlaneData)
			if name == "a later expiry" {
				// A later expiry is not a widening the token can express: the
				// earlier one stands.
				if err != nil || got.ExpiresAt.After(time.Now().Add(2*time.Hour)) {
					t.Fatalf("later expiry: %v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrBiscuitAttenuation) || !errors.Is(err, ErrInvalidSignature) {
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
	if _, err := v.Verify(context.Background(), tok, AudiencePlaneData); !errors.Is(err, ErrBiscuitAttenuation) {
		t.Fatalf("datalog check: err = %v, want ErrBiscuitAttenuation", err)
	}

	b, _ = parseBiscuit(token)
	blk = b.CreateBlock()
	_ = blk.AddFact(biscuitFact("right", biscuit.String("everything")))
	withFact, _ := b.Append(rand.Reader, blk.Build())
	tok, _ = serializeBiscuit(withFact)
	if _, err := v.Verify(context.Background(), tok, AudiencePlaneData); !errors.Is(err, ErrBiscuitAttenuation) {
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
	if _, err := v.Verify(context.Background(), inner, AudiencePlaneData); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("sealed token alone: err = %v, want ErrInvalidSignature", err)
	}

	// Re-wrapping that JWT in a Biscuit of one's own fails: its root is not
	// the one the JWT vouches for.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	bb := biscuit.NewBuilder(priv)
	_ = bb.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(inner)))
	forged, _ := bb.Build()
	tok, _ := serializeBiscuit(forged)
	if _, err := v.Verify(context.Background(), tok, AudiencePlaneData); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("re-wrapped: err = %v, want ErrInvalidSignature", err)
	}

	// A Biscuit around an ordinary token (no root claim) is refused too.
	_, plain, _ := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: cap.Subject,
		Audience: []string{AudiencePlaneData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
	})
	bb = biscuit.NewBuilder(priv)
	_ = bb.AddAuthorityFact(biscuitFact(biscuitFactCapability, biscuit.String(plain)))
	wrapped, _ := bb.Build()
	tok, _ = serializeBiscuit(wrapped)
	if _, err := v.Verify(context.Background(), tok, AudiencePlaneData); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("wrapped plain token: err = %v, want ErrInvalidSignature", err)
	}

	// Tampering with any byte breaks the chain.
	raw, _ := base64.RawURLEncoding.DecodeString(narrowed)
	raw[len(raw)-3] ^= 1
	if _, err := v.Verify(context.Background(), base64.RawURLEncoding.EncodeToString(raw), AudiencePlaneData); err == nil {
		t.Fatal("tampered Biscuit accepted")
	}
}

func TestBiscuitRevokedWithItsCapability(t *testing.T) {
	_, v, store, cap, token := biscuitFixture(t)
	narrowed, _ := Attenuate(token, Attenuation{Ops: []Op{OpGet}})
	if err := store.Revoke(context.Background(), RevokeRequest{ID: cap.ID, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), narrowed, AudiencePlaneData); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}

func TestBiscuitOfflineBinding(t *testing.T) {
	_, v, _, _, token := biscuitFixture(t)
	keys := dpopKeys(t)
	jkt, _ := KeyThumbprint(keys["Ed25519"].Public())
	bound, err := Attenuate(token, Attenuation{ConfirmationJKT: jkt})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), bound, AudiencePlaneData)
	if err != nil || got.ConfirmationJKT != jkt {
		t.Fatalf("bound offline: %q, %v", got.ConfirmationJKT, err)
	}
	other, _ := KeyThumbprint(keys["P-256"].Public())
	rebound, _ := Attenuate(bound, Attenuation{ConfirmationJKT: other})
	if _, err := v.Verify(context.Background(), rebound, AudiencePlaneData); !errors.Is(err, ErrBiscuitAttenuation) {
		t.Fatalf("rebinding offline: err = %v, want ErrBiscuitAttenuation", err)
	}
}

func TestVerifierRefusesBiscuitUnlessEnabled(t *testing.T) {
	_, plainVerifier, _, _ := buildIssuerVerifier(t)
	_, _, _, _, token := biscuitFixture(t)
	if _, err := plainVerifier.Verify(context.Background(), token, AudiencePlaneData); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

// Whatever chain of blocks a holder appends, the verified result is either
// refused or within the original: the delegation fuzz property, over chains.
func TestBiscuitAttenuationChainsNeverWiden(t *testing.T) {
	_, v, _, cap, token := biscuitFixture(t)
	r := mathrand.New(mathrand.NewPCG(7, 11))
	ops := []Op{OpGet, OpList, OpPut, OpDelete, OpShare}
	prefixes := []string{"corpus/", "corpus/public/", "corpus/public/a", "other/", "corpus-x/", "corpus/private/"}
	planes := []string{AudiencePlaneData, AudiencePlaneMCP, AudiencePlaneAdmin}
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
				a.Planes = pick(planes)
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
			got, err := v.Verify(context.Background(), tok, plane)
			if err != nil {
				continue
			}
			verified++
			if err := Narrows(cap, got); err != nil {
				t.Fatalf("a verified chain widened its capability: %v\n%+v", err, got.Caveats)
			}
		}
	}
	if verified == 0 {
		t.Fatal("no chain verified; the property was never exercised")
	}
}
