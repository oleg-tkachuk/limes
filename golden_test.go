package limes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// The wire-format guard.
//
// The fixtures (goldenFiles) are minted from the fixed seed and clock of
// goldengen_test.go, one per token type. If these tests fail, a token format
// changed — which invalidates capabilities already held by running agents,
// not merely a build.
//
// Regenerating the fixture is a deliberate act, not a fix: it declares the old
// format dead. Do it only with the format change reviewed on its own merits.

func TestGoldenTokenStillVerifies(t *testing.T) {
	for typ := range goldenFiles {
		t.Run(string(typ), func(t *testing.T) { checkGoldenVerifies(t, typ) })
	}
}

func checkGoldenVerifies(t *testing.T, typ TokenType) {
	raw, err := os.ReadFile(goldenPath(typ))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	token := string(raw)

	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)

	verifier, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		// Pinned to the issuance instant so nbf/exp stay inside the window
		// forever — the fixture tests format, not lifetime.
		Now: goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	cap, err := verifier.Verify(context.Background(), VerifyRequest{Token: token, Audience: goldenAudience})
	if err != nil {
		t.Fatalf("golden token no longer verifies — the wire format changed: %v", err)
	}

	// Decode, not just accept: a format change could keep the signature valid
	// while relocating or renaming a claim, which acceptance alone would miss.
	if cap.Capability().Issuer != goldenIssuer {
		t.Errorf("Issuer = %q, want %q", cap.Capability().Issuer, goldenIssuer)
	}
	if cap.Capability().Subject.Subject != goldenSubject {
		t.Errorf("Subject = %q, want %q", cap.Capability().Subject.Subject, goldenSubject)
	}
	if cap.Capability().Subject.Type != PrincipalTypeAgent {
		t.Errorf("principal kind = %q, want agent", cap.Capability().Subject.Type)
	}
	if cap.Capability().Subject.TenantID.String() != goldenTenant {
		t.Errorf("TenantID = %q, want %q", cap.Capability().Subject.TenantID, goldenTenant)
	}
	if len(cap.Capability().Caveats.Ops) != 1 || cap.Capability().Caveats.Ops[0] != OpGet {
		t.Errorf("Ops = %v, want [%v]", cap.Capability().Caveats.Ops, OpGet)
	}
	if cap.Capability().Caveats.Requests != (RequestCeiling{Max: 10}) {
		t.Errorf("Requests = %v, want 10", cap.Capability().Caveats.Requests)
	}
	if cap.Capability().Caveats.Budget != (BudgetCeiling{Max: 3 * NanosPerUnit / 2, UnitCode: "USD"}) { // 1.5, as the fixture was issued
		t.Errorf("MaxBudgetAmount = %v, want 1.5 USD", cap.Capability().Caveats.Budget)
	}
	// The unit code is what cross-currency delegation rejection keys on.
	if cap.Capability().Caveats.Budget.UnitCode != "USD" {
		t.Errorf("UnitCode = %q, want USD", cap.Capability().Caveats.Budget.UnitCode)
	}
	if len(cap.Capability().Caveats.ResourcePrefixes) != 1 || cap.Capability().Caveats.ResourcePrefixes[0] != "golden/" {
		t.Errorf("ResourcePrefixes = %v", cap.Capability().Caveats.ResourcePrefixes)
	}
}

// A token for one audience must not verify for another — the audience gate is
// part of the format contract, not only of the runtime policy.
func TestGoldenTokenRejectsWrongAudience(t *testing.T) {
	for typ := range goldenFiles {
		t.Run(string(typ), func(t *testing.T) { checkGoldenRejectsWrongAudience(t, typ) })
	}
}

func checkGoldenRejectsWrongAudience(t *testing.T, typ TokenType) {
	raw, err := os.ReadFile(goldenPath(typ))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(goldenSeed[:])
	pub := priv.Public().(ed25519.PublicKey)

	verifier, err := NewVerifier(VerifierConfig{
		Keys:           staticKeys(t, map[string]ed25519.PublicKey{goldenKID: pub}),
		Revocations:    revLookup{},
		TrustedIssuers: []string{goldenIssuer},
		Now:            goldenClock,
	})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), VerifyRequest{Token: string(raw), Audience: "limes-admin"}); err == nil {
		t.Fatal("a token minted for limes-data must not verify for limes-admin")
	}
}

// The claims round-trip byte for byte: decoding the golden payload and
// writing it back as Sign would gives the signed bytes. A renamed member, a
// changed omission rule or a moved claim fails here even where the decoded
// fields still compare equal. Only the format Sign writes round-trips: the
// v4 fixture's limes_gen is read and dropped.
func TestGoldenClaimsRoundTripExactly(t *testing.T) {
	raw, err := os.ReadFile(goldenPath(TokenTypeV5))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	parts, err := jose.Split(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseClaims(parts.Payload, TokenTypeV5)
	if err != nil {
		t.Fatal(err)
	}
	written, err := json.Marshal(claimsOf(*c))
	if err != nil {
		t.Fatal(err)
	}
	signed, err := base64.RawURLEncoding.DecodeString(parts.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, signed) {
		t.Errorf("claims written back differ from the signed ones:\n got %s\nwant %s", written, signed)
	}
}
