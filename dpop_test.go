package limes

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/dpop"
)

const (
	dpopURL    = "https://data.example.com/example.data.v1.ObjectService/GetObject"
	dpopMethod = "POST"
)

// withProof is a VerifyRequest for token carrying a fresh proof from key,
// issued at now.
func withProof(ctx context.Context, t *testing.T, key crypto.Signer, token, audience string, now time.Time) VerifyRequest {
	t.Helper()
	proof, err := dpop.Sign(ctx, key, dpop.ProofRequest{Method: dpopMethod, URL: dpopURL, Token: token, IssuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return VerifyRequest{Token: token, Audience: audience, DPoP: dpop.Proof{JWT: proof, Method: dpopMethod, URL: dpopURL}}
}

func dpopKeys(t *testing.T) map[string]crypto.Signer {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]crypto.Signer{"Ed25519": ed, "P-256": ec}
}

func TestKeyBindingSurvivesTheTokenAndDelegation(t *testing.T) {
	issuer, verifier, _, _ := buildIssuerVerifier(t)
	ctx := context.Background()
	key := dpopKeys(t)["Ed25519"]
	jkt, _ := dpop.Thumbprint(key.Public())

	parent, token, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet, OpShare}}, TTL: time.Hour,
		ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(ctx, VerifyRequest{Token: token, Audience: audienceData}); !errors.Is(err, dpop.ErrRequired) {
		t.Fatalf("bound token without a proof: err = %v, want dpop.ErrRequired", err)
	}
	got, err := verifier.Verify(ctx, withProof(t.Context(), t, key, token, audienceData, time.Now()))
	if err != nil || got.Capability().ConfirmationJKT != jkt {
		t.Fatalf("verified binding = %q, %v; want %q", got.Capability().ConfirmationJKT, err, jkt)
	}

	child, _, err := issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true})
	if err != nil || child.ConfirmationJKT != jkt {
		t.Fatalf("child without its own key: binding %q, %v; want the parent's", child.ConfirmationJKT, err)
	}
	subKey := dpopKeys(t)["P-256"]
	subJKT, _ := dpop.Thumbprint(subKey.Public())
	child, _, err = issuer.delegate(ctx, DelegateRequest{Subject: Principal{Type: PrincipalTypeAgent, Subject: "child"}, Parent: asVerified(parent), InheritCaveats: true, ConfirmationJKT: subJKT})
	if err != nil || child.ConfirmationJKT != subJKT {
		t.Fatalf("child bound to the sub-agent's key: %q, %v", child.ConfirmationJKT, err)
	}

	unbound := child
	unbound.ConfirmationJKT = ""
	if err := Narrows(parent, unbound); !errors.Is(err, ErrDelegationTooWide) {
		t.Fatalf("unbound child of a bound parent: err = %v, want ErrDelegationTooWide", err)
	}
}

func TestIssueRefusesAMalformedBinding(t *testing.T) {
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour,
		ConfirmationJKT: "not-a-thumbprint",
	})
	if err == nil {
		t.Fatal("malformed ConfirmationJKT accepted")
	}
}

// newReplay is dpop.NewMemoryReplayCache, failing the test on a refused
// configuration.
func newReplay(t testing.TB, cfg dpop.MemoryReplayCacheConfig) *dpop.MemoryReplayCache {
	t.Helper()
	c, err := dpop.NewMemoryReplayCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newVerifier is dpop.NewVerifier, failing the test on a refused
// configuration.
func newVerifier(t testing.TB, cfg dpop.VerifierConfig) *dpop.Verifier {
	t.Helper()
	v, err := dpop.NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// replayShare is the per-holder room of the replay caches the revoked-flood
// tests use: small, so a flood that spends it is a few proofs.
const replayShare = 3

// replayVerifier is a verifier over store whose replay cache holds
// replayShare proofs per holder and maxEntries in all.
func replayVerifier(t *testing.T, store *memStore, pub ed25519.PublicKey, maxEntries int) *Verifier {
	t.Helper()
	v, err := NewVerifier(VerifierConfig{
		Keys: staticKeys(t, map[string]ed25519.PublicKey{"k1": pub}), Revocations: store,
		TrustedIssuers: []string{"limes-test"}, AcceptBiscuit: true, BiscuitRevocations: store,
		DPoP: newVerifier(t, dpop.VerifierConfig{Replay: newReplay(t, dpop.MemoryReplayCacheConfig{
			EntriesPerHolder: replayShare, MaxEntries: maxEntries,
		})}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A revoked copy keeps no claim on the replay room its capability's copies
// share: its proofs are refused as revoked without being recorded, so a
// live sibling bound to another key still verifies.
func TestARevokedCopySpendsNoReplayRoom(t *testing.T) {
	ctx := context.Background()
	issuer, _, store, pub := buildIssuerVerifier(t)
	cap, _, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, TTL: time.Hour,
		Caveats: Caveats{Ops: []Op{OpGet}, Requests: RequestCeiling{Unlimited: true}, Budget: BudgetCeiling{Unlimited: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := issuer.biscuit(ctx, asVerified(cap))
	if err != nil {
		t.Fatal(err)
	}
	v := replayVerifier(t, store, pub, 10*replayShare)
	keys := dpopKeys(t)
	liveKey, revokedKey := keys["Ed25519"], keys["P-256"]
	liveJKT, _ := dpop.Thumbprint(liveKey.Public())
	revokedJKT, _ := dpop.Thumbprint(revokedKey.Public())
	live, err := Attenuate(root, Attenuation{ConfirmationJKT: liveJKT})
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := Attenuate(root, Attenuation{ConfirmationJKT: revokedJKT})
	if err != nil {
		t.Fatal(err)
	}
	revokeCopy(t, v, store, revoked)

	for i := range replayShare {
		if _, err := v.Verify(ctx, withProof(ctx, t, revokedKey, revoked, audienceData, time.Now())); !errors.Is(err, ErrRevoked) {
			t.Fatalf("revoked copy, proof %d: err = %v, want ErrRevoked", i+1, err)
		}
	}
	if _, err := v.Verify(ctx, withProof(ctx, t, liveKey, live, audienceData, time.Now())); err != nil {
		t.Fatalf("live sibling after the revoked copy's proofs: %v", err)
	}
}

// Revoked capabilities of one tenant do not fill the replay cache's overall
// bound and lock out another tenant's live capability.
func TestRevokedCapabilitiesSpendNoReplayRoom(t *testing.T) {
	const revokedCaps = 2
	ctx := context.Background()
	issuer, _, store, pub := buildIssuerVerifier(t)
	v := replayVerifier(t, store, pub, revokedCaps*replayShare)
	keys := dpopKeys(t)
	revokedKey, liveKey := keys["P-256"], keys["Ed25519"]
	issue := func(tenant uuid.UUID, key crypto.Signer) (Capability, string) {
		t.Helper()
		jkt, _ := dpop.Thumbprint(key.Public())
		c, tok, err := issuer.issue(ctx, IssueRequest{
			IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: tenant, Subject: "a"},
			Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, ConfirmationJKT: jkt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c, tok
	}

	flooder := uuid.New()
	for range revokedCaps {
		c, tok := issue(flooder, revokedKey)
		if err := store.Revoke(ctx, RevokeRequest{CapabilityID: c.ID, Note: "test"}); err != nil {
			t.Fatal(err)
		}
		for i := range replayShare {
			if _, err := v.Verify(ctx, withProof(ctx, t, revokedKey, tok, audienceData, time.Now())); !errors.Is(err, ErrRevoked) {
				t.Fatalf("revoked capability, proof %d: err = %v, want ErrRevoked", i+1, err)
			}
		}
	}
	_, tok := issue(uuid.New(), liveKey)
	if _, err := v.Verify(ctx, withProof(ctx, t, liveKey, tok, audienceData, time.Now())); err != nil {
		t.Fatalf("another tenant's live capability after the revoked flood: %v", err)
	}
}

// A key-bound token's proof is checked before its revocation is looked up:
// a holder of the token without its key is refused for the proof, learns
// nothing of whether the token is revoked, and causes no lookup.
func TestAProofIsCheckedBeforeRevocation(t *testing.T) {
	ctx := context.Background()
	issuer, _, store, pub := buildIssuerVerifier(t)
	v := replayVerifier(t, store, pub, 10*replayShare)
	keys := dpopKeys(t)
	boundKey, otherKey := keys["Ed25519"], keys["P-256"]
	jkt, _ := dpop.Thumbprint(boundKey.Public())
	c, tok, err := issuer.issue(ctx, IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{Type: PrincipalTypeAgent, TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, TTL: time.Hour, ConfirmationJKT: jkt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, RevokeRequest{CapabilityID: c.ID, Note: "test"}); err != nil {
		t.Fatal(err)
	}

	for name, req := range map[string]VerifyRequest{
		"no proof":            {Token: tok, Audience: audienceData},
		"another key's proof": withProof(ctx, t, otherKey, tok, audienceData, time.Now()),
	} {
		if _, err := v.Verify(ctx, req); !errors.Is(err, ErrProofOfPossession) || errors.Is(err, ErrRevoked) {
			t.Errorf("revoked token, %s: err = %v, want ErrProofOfPossession", name, err)
		}
	}
	if n := store.revCalls.Load(); n != 0 {
		t.Fatalf("revocation lookups before a proof passed = %d, want 0", n)
	}
	if _, err := v.Verify(ctx, withProof(ctx, t, boundKey, tok, audienceData, time.Now())); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked token, the bound key's proof: err = %v, want ErrRevoked", err)
	}
}
