package limes

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// The wire-format guard.
//
// testdata/golden_token.jwt is minted by goldengen_test.go from the fixed seed
// and clock below. If this test fails, the token format changed — which
// invalidates capabilities already held by running agents, not merely a build.
//
// Regenerating the fixture is a deliberate act, not a fix: it declares the old
// format dead. Do it only with the format change reviewed on its own merits.

func TestGoldenTokenStillVerifies(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_token.jwt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	token := string(raw)

	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)

	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		// Pinned to the issuance instant so nbf/exp stay inside the window
		// forever — the fixture tests format, not lifetime.
		Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	cap, err := verifier.Verify(context.Background(), token, goldenAudience)
	if err != nil {
		t.Fatalf("golden token no longer verifies — the wire format changed: %v", err)
	}

	// Decode, not just accept: a format change could keep the signature valid
	// while relocating or renaming a claim, which acceptance alone would miss.
	if cap.Issuer != goldenIssuer {
		t.Errorf("Issuer = %q, want %q", cap.Issuer, goldenIssuer)
	}
	if cap.Subject.Subject != goldenSubject {
		t.Errorf("Subject = %q, want %q", cap.Subject.Subject, goldenSubject)
	}
	if cap.Subject.Type != PrincipalAgent {
		t.Errorf("principal kind = %q, want agent", cap.Subject.Type)
	}
	if cap.Subject.TenantID.String() != goldenTenant {
		t.Errorf("TenantID = %q, want %q", cap.Subject.TenantID, goldenTenant)
	}
	if len(cap.Caveats.Ops) != 1 || cap.Caveats.Ops[0] != OpGet {
		t.Errorf("Ops = %v, want [%v]", cap.Caveats.Ops, OpGet)
	}
	if cap.Caveats.MaxRequests != 10 {
		t.Errorf("MaxRequests = %d, want 10", cap.Caveats.MaxRequests)
	}
	if cap.Caveats.MaxBudgetAmount != 3*NanosPerUnit/2 { // 1.5, as the fixture was issued
		t.Errorf("MaxBudgetAmount = %v, want 1.5", cap.Caveats.MaxBudgetAmount)
	}
	// The unit code is what cross-currency delegation rejection keys on.
	if cap.Caveats.UnitCode != "USD" {
		t.Errorf("UnitCode = %q, want USD", cap.Caveats.UnitCode)
	}
	if len(cap.Caveats.ResourcePrefixes) != 1 || cap.Caveats.ResourcePrefixes[0] != "golden/" {
		t.Errorf("ResourcePrefixes = %v", cap.Caveats.ResourcePrefixes)
	}
}

// A token for one audience must not verify for another — the audience gate is
// part of the format contract, not only of the runtime policy.
func TestGoldenTokenRejectsWrongAudience(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_token.jwt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)

	verifier, err := NewStandardVerifier(VerifierConfig{
		Keys:           NewStaticKeyResolver(map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		Now:            goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), string(raw), "paladin-admin"); err == nil {
		t.Fatal("a token minted for paladin-data must not verify for paladin-admin")
	}
}
