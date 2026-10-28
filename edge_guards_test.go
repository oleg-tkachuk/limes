package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/oleg-tkachuk/limes/jwks"
)

// Guards for edge cases no other test covers.

// newKID is the kid of the key rotated in.
const newKID = jwks.KeyIDPrefix + "new"

func testIssuer(t *testing.T, kid string, priv ed25519.PrivateKey, now func() time.Time) *Issuer {
	t.Helper()
	signer, err := NewSigner(SignerConfig{KeyID: kid, Key: SealKey(priv)})
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	iss, err := NewIssuer(IssuerConfig{
		Signer: signer, Store: genStore{}, IssuerName: goldenIssuer,
		DefaultTTL: time.Hour, Now: now,
	})
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	return iss
}

func mintToken(t *testing.T, iss *Issuer) string {
	t.Helper()
	_, token, err := iss.issue(context.Background(), IssueRequest{ClientID: testClientID,
		IssuedBy: Principal{Subject: "test-operator"},
		Subject: Principal{
			Type: PrincipalTypeService, TenantID: uuid.MustParse(goldenTenant), Subject: "svc",
		},
		Audience: []string{goldenAudience},
		Caveats:  Caveats{Ops: []Op{OpGet}},
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return token
}

// ─── edge case 3: signing key rotation ─────────────────────────────────────

// Rotation is only non-disruptive if a token signed under the OLD key keeps
// verifying while that key remains published. quickstart.md documents a
// three-step procedure; nothing tested it, and getting the withdrawal deadline
// wrong invalidates tokens agents are still holding.
func TestRetiredButPublishedKeyStillVerifies(t *testing.T) {
	oldPriv := ed25519.NewKeyFromSeed(goldenSeed[:])
	oldPub := oldPriv.Public().(ed25519.PublicKey)

	newPriv := goldenKeyFlipped(0)
	newPub := newPriv.Public().(ed25519.PublicKey)

	// A token minted under the old key, before rotation.
	oldToken := mintToken(t, testIssuer(t, goldenKID, oldPriv, goldenClock))

	// Step 1 of rotation: publish the new key alongside the old.
	resolver := staticKeys(t, map[string]ed25519.PublicKey{goldenKID: oldPub})
	if err := resolver.SetKey(newKID, newPub); err != nil {
		t.Fatal(err)
	}

	verifier, err := NewVerifier(VerifierConfig{
		Keys: resolver, Revocations: revLookup{},
		TrustedIssuers: []string{goldenIssuer}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), VerifyRequest{Token: oldToken, Audience: goldenAudience}); err != nil {
		t.Fatalf("token under a retired-but-published key must still verify: %v", err)
	}

	// Step 2: new signing key issues tokens that also verify.
	newToken := mintToken(t, testIssuer(t, newKID, newPriv, goldenClock))
	if _, err := verifier.Verify(context.Background(), VerifyRequest{Token: newToken, Audience: goldenAudience}); err != nil {
		t.Fatalf("token under the new key must verify: %v", err)
	}
}

// Step 3: once withdrawn, tokens signed under the old key stop verifying.
// This is why the withdrawal deadline is max-outstanding-TTL, not "now".
func TestWithdrawnKeyStopsVerifying(t *testing.T) {
	oldPriv := ed25519.NewKeyFromSeed(goldenSeed[:])
	oldToken := mintToken(t, testIssuer(t, goldenKID, oldPriv, goldenClock))

	newPub := goldenKeyFlipped(0).Public().(ed25519.PublicKey)

	// The old kid is simply absent from the published set.
	if err := verifyGoldenUnder(t, oldToken, map[string]ed25519.PublicKey{newKID: newPub}); err == nil {
		t.Fatal("a token under a withdrawn key must stop verifying")
	}
}

// ─── edge case 5: missing record is forgery ────────────────────────────────

// jwks.Static reports an unknown kid distinctly, so a forged token
// signed by an unpublished key is separable from a malformed one.
func TestUnknownKIDIsDistinct(t *testing.T) {
	r := staticKeys(t, map[string]ed25519.PublicKey{})
	_, err := r.PublicKey(context.Background(), "never-published")
	if !errors.Is(err, jwks.ErrUnknownKID) {
		t.Fatalf("want jwks.ErrUnknownKID, got %v", err)
	}
}

// A token whose signature does not match the published key must be rejected as
// invalid rather than accepted — the forgery case in its most direct form.
func TestForgedSignatureRejected(t *testing.T) {
	realPriv := ed25519.NewKeyFromSeed(goldenSeed[:])
	token := mintToken(t, testIssuer(t, goldenKID, realPriv, goldenClock))

	// Publish a DIFFERENT key under the same kid — i.e. the token was signed
	// by someone who is not the issuer we trust.
	otherPub := goldenKeyFlipped(len(goldenSeed) - 1).Public().(ed25519.PublicKey)
	if err := verifyGoldenUnder(t, token, map[string]ed25519.PublicKey{goldenKID: otherPub}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("want ErrInvalidToken for a forged token, got %v", err)
	}
}

// An untrusted issuer must be refused even when the signature is valid — a
// multi-tenant control plane deploying several issuers depends on it.
func TestUntrustedIssuerRejected(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)
	token := mintToken(t, testIssuer(t, goldenKID, priv, goldenClock))

	verifier, err := NewVerifier(VerifierConfig{
		Keys:        staticKeys(t, map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations: revLookup{}, TrustedIssuers: []string{"some-other-issuer"}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), VerifyRequest{Token: token, Audience: goldenAudience}); err == nil {
		t.Fatal("a token from an untrusted issuer must not verify")
	}
}

// ─── unit codes and sentinel distinguishability ────────────────────────────

// IsUnitCode is published API: the one spelling of each ISO 4217 code,
// which caveats accept beside an empty unit — none named, never one assumed.
func TestUnitCodes(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"USD", true},
		{"EUR", true},
		{"UAH", true},
		{"JPY", true}, // any ISO 4217 code, not a list of four
		{"CHF", true},
		{AbstractUnitCode, true},
		{"usd", false}, // one spelling per unit, so counters compare
		{"XYZ", false},
		{"BTC", false},
		{"US", false},
		{"UNIT", false},
	}
	for _, tc := range cases {
		if IsUnitCode(tc.in) != tc.ok {
			t.Errorf("IsUnitCode(%q) = %v, want %v", tc.in, !tc.ok, tc.ok)
		}
		if err := (Caveats{Ops: []Op{OpGet}, Budget: BudgetCeiling{UnitCode: tc.in}}).Validate(); (err == nil) != tc.ok {
			t.Errorf("caveats in %q: err = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
	// Empty is no unit code, and caveats that name none are valid.
	if IsUnitCode("") {
		t.Error(`IsUnitCode("") must be false`)
	}
	if err := (Caveats{Ops: []Op{OpGet}}).Validate(); err != nil {
		t.Errorf("caveats naming no unit: %v", err)
	}
}

// Every rejection a consumer maps to a transport code must
// stay individually matchable. Identity here is the contract — an errors.Is
// that starts matching two sentinels collapses two distinct client outcomes.
// Family roots (ErrNotFound and the others) are left out: their members match
// them by design, and TestSentinelMatrix pins exactly which.
func TestSentinelsAreIndividuallyMatchable(t *testing.T) {
	all := map[string]error{
		"ErrInvalidToken":         ErrInvalidToken,
		"ErrExpired":              ErrExpired,
		"ErrNotYetValid":          ErrNotYetValid,
		"ErrRevoked":              ErrRevoked,
		"ErrCaveatViolation":      ErrCaveatViolation,
		"ErrAudienceMismatch":     ErrAudienceMismatch,
		"ErrBudgetExceeded":       ErrBudgetExceeded,
		"ErrDelegationTooWide":    ErrDelegationTooWide,
		"ErrUnitCodeMismatch":     ErrUnitCodeMismatch,
		"ErrTenantBudgetExceeded": ErrTenantBudgetExceeded,
		"ErrRequestLimitExceeded": ErrRequestLimitExceeded,
		"jwks.ErrUnknownKID":      jwks.ErrUnknownKID,
	}
	if len(all) != 12 {
		t.Fatalf("expected 12 sentinels, listed %d", len(all))
	}
	for nameA, a := range all {
		for nameB, b := range all {
			if nameA == nameB {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("%s matches %s — they must stay distinguishable", nameA, nameB)
			}
		}
		// Wrapping must preserve identity; callers wrap with context.
		if !errors.Is(errors.Join(errors.New("ctx"), a), a) {
			t.Errorf("%s does not survive wrapping", nameA)
		}
	}
}

// goldenKeyFlipped is the key of goldenSeed with its byte at i inverted: a
// key that is not the golden issuer's.
func goldenKeyFlipped(i int) ed25519.PrivateKey {
	seed := goldenSeed
	seed[i] ^= 0xff
	return ed25519.NewKeyFromSeed(seed[:])
}

// verifyGoldenUnder verifies token, for the golden audience, with a verifier
// that trusts the golden issuer and publishes keys alone.
func verifyGoldenUnder(t *testing.T, token string, keys map[string]ed25519.PublicKey) error {
	t.Helper()
	verifier, err := NewVerifier(VerifierConfig{
		Keys:        staticKeys(t, keys),
		Revocations: revLookup{}, TrustedIssuers: []string{goldenIssuer}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	_, err = verifier.Verify(context.Background(), VerifyRequest{Token: token, Audience: goldenAudience})
	return err
}
