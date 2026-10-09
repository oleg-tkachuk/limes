package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Guards for edge cases no other test covers.

func testIssuer(t *testing.T, kid string, priv ed25519.PrivateKey, now func() time.Time) *Issuer {
	t.Helper()
	signer, err := NewEd25519Signer(kid, priv)
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
	_, token, err := iss.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "test-operator"},
		Subject: Principal{
			Type: PrincipalService, TenantID: uuid.MustParse(goldenTenant), Subject: "svc",
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

	var newSeed [32]byte
	copy(newSeed[:], goldenSeed[:])
	newSeed[0] ^= 0xff
	newPriv := ed25519.NewKeyFromSeed(newSeed[:])
	newPub := newPriv.Public().(ed25519.PublicKey)

	// A token minted under the old key, before rotation.
	oldToken := mintToken(t, testIssuer(t, goldenKID, oldPriv, goldenClock))

	// Step 1 of rotation: publish the new key alongside the old.
	resolver := NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: oldPub})
	resolver.SetKey("new-kid", newPub)

	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys: resolver, Revocations: revLookup{},
		TrustedIssuers: []string{goldenIssuer}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), oldToken, goldenAudience); err != nil {
		t.Fatalf("token under a retired-but-published key must still verify: %v", err)
	}

	// Step 2: new signing key issues tokens that also verify.
	newToken := mintToken(t, testIssuer(t, "new-kid", newPriv, goldenClock))
	if _, err := verifier.Verify(context.Background(), newToken, goldenAudience); err != nil {
		t.Fatalf("token under the new key must verify: %v", err)
	}
}

// Step 3: once withdrawn, tokens signed under the old key stop verifying.
// This is why the withdrawal deadline is max-outstanding-TTL, not "now".
func TestWithdrawnKeyStopsVerifying(t *testing.T) {
	oldPriv := ed25519.NewKeyFromSeed(goldenSeed[:])
	oldToken := mintToken(t, testIssuer(t, goldenKID, oldPriv, goldenClock))

	var newSeed [32]byte
	copy(newSeed[:], goldenSeed[:])
	newSeed[0] ^= 0xff
	newPub := ed25519.NewKeyFromSeed(newSeed[:]).Public().(ed25519.PublicKey)

	// The old kid is simply absent from the published set.
	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:        NewStaticKeyResolver(map[string]ed25519.PublicKey{"new-kid": newPub}),
		Revocations: revLookup{}, TrustedIssuers: []string{goldenIssuer}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), oldToken, goldenAudience); err == nil {
		t.Fatal("a token under a withdrawn key must stop verifying")
	}
}

// ─── edge case 5: missing record is forgery ────────────────────────────────

// StaticKeyResolver reports an unknown kid distinctly, so a forged token
// signed by an unpublished key is separable from a malformed one.
func TestUnknownKIDIsDistinct(t *testing.T) {
	r := NewStaticKeyResolver(map[string]ed25519.PublicKey{})
	_, err := r.PublicKey(context.Background(), "never-published")
	if !errors.Is(err, ErrUnknownKID) {
		t.Fatalf("want ErrUnknownKID, got %v", err)
	}
}

// A token whose signature does not match the published key must be rejected as
// invalid rather than accepted — the forgery case in its most direct form.
func TestForgedSignatureRejected(t *testing.T) {
	realPriv := ed25519.NewKeyFromSeed(goldenSeed[:])
	token := mintToken(t, testIssuer(t, goldenKID, realPriv, goldenClock))

	// Publish a DIFFERENT key under the same kid — i.e. the token was signed
	// by someone who is not the issuer we trust.
	var otherSeed [32]byte
	copy(otherSeed[:], goldenSeed[:])
	otherSeed[31] ^= 0xff
	otherPub := ed25519.NewKeyFromSeed(otherSeed[:]).Public().(ed25519.PublicKey)

	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:        NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: otherPub}),
		Revocations: revLookup{}, TrustedIssuers: []string{goldenIssuer}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), token, goldenAudience); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("want ErrInvalidSignature for a forged token, got %v", err)
	}
}

// An untrusted issuer must be refused even when the signature is valid — a
// multi-tenant control plane deploying several Paladin instances depends on it.
func TestUntrustedIssuerRejected(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)
	token := mintToken(t, testIssuer(t, goldenKID, priv, goldenClock))

	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:        NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations: revLookup{}, TrustedIssuers: []string{"some-other-paladin"}, Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), token, goldenAudience); err == nil {
		t.Fatal("a token from an untrusted issuer must not verify")
	}
}

// ─── unit codes and sentinel distinguishability ────────────────────────────

// NormaliseUnitCode / IsAllowedUnitCode are published API. They gate the
// empty-means-default rule and the comparison behind ErrUnitCodeMismatch.
func TestNormaliseUnitCode(t *testing.T) {
	t.Run("empty resolves to the default", func(t *testing.T) {
		got, err := NormaliseUnitCode("")
		if err != nil || got != DefaultUnitCode {
			t.Errorf("got %q, %v; want %q, nil", got, err, DefaultUnitCode)
		}
	})
	t.Run("every allowed code round-trips", func(t *testing.T) {
		for _, u := range AllowedUnitCodes {
			got, err := NormaliseUnitCode(u)
			if err != nil || got != u {
				t.Errorf("NormaliseUnitCode(%q) = %q, %v", u, got, err)
			}
		}
	})
	t.Run("unknown code errors", func(t *testing.T) {
		if _, err := NormaliseUnitCode("XYZ"); err == nil {
			t.Error("want an error for an unknown unit")
		}
	})
}

// The asymmetry is deliberate: NormaliseUnitCode("") is the default, but
// IsAllowedUnitCode("") is false. Callers wanting empty-means-default must
// normalise first, and pinning this stops someone "fixing" the inconsistency.
func TestIsAllowedUnitCodeAsymmetry(t *testing.T) {
	if IsAllowedUnitCode("") {
		t.Error(`IsAllowedUnitCode("") must be false`)
	}
	if got, _ := NormaliseUnitCode(""); got != DefaultUnitCode {
		t.Error(`NormaliseUnitCode("") must yield the default`)
	}
	for _, u := range AllowedUnitCodes {
		if !IsAllowedUnitCode(u) {
			t.Errorf("IsAllowedUnitCode(%q) = false", u)
		}
	}
	if IsAllowedUnitCode("XYZ") {
		t.Error(`IsAllowedUnitCode("XYZ") must be false`)
	}
}

// Every rejection a consumer maps to a transport code must
// stay individually matchable. Identity here is the contract — an errors.Is
// that starts matching two sentinels collapses two distinct client outcomes.
func TestSentinelsAreIndividuallyMatchable(t *testing.T) {
	all := map[string]error{
		"ErrInvalidSignature":     ErrInvalidSignature,
		"ErrExpired":              ErrExpired,
		"ErrNotYetValid":          ErrNotYetValid,
		"ErrRevoked":              ErrRevoked,
		"ErrCaveatViolation":      ErrCaveatViolation,
		"ErrAudienceMismatch":     ErrAudienceMismatch,
		"ErrBudgetExceeded":       ErrBudgetExceeded,
		"ErrDelegationTooWide":    ErrDelegationTooWide,
		"ErrUnitCodeMismatch":     ErrUnitCodeMismatch,
		"ErrNotFound":             ErrNotFound,
		"ErrUsageNotFound":        ErrUsageNotFound,
		"ErrTenantBudgetNotFound": ErrTenantBudgetNotFound,
		"ErrTenantBudgetExceeded": ErrTenantBudgetExceeded,
		"ErrRequestLimitExceeded": ErrRequestLimitExceeded,
		"ErrUnknownKID":           ErrUnknownKID,
	}
	if len(all) != 15 {
		t.Fatalf("expected 15 sentinels, listed %d — update contracts §4 too", len(all))
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
