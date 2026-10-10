package limes

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// RFC 7493 §2.2: a number in a token is an integer a double holds exactly,
// so a reader in any language reads the value that was signed.
func TestTokenIntegersStayWithinIJSON(t *testing.T) {
	if err := (Caveats{Ops: []Op{OpGet}, MaxRequests: MaxIJSONInteger}).Validate(); err != nil {
		t.Errorf("the largest I-JSON integer refused: %v", err)
	}
	if err := (Caveats{Ops: []Op{OpGet}, MaxRequests: MaxIJSONInteger + 1}).Validate(); !errors.Is(err, ErrInvalidCaveats) {
		t.Errorf("max_requests past 2^53-1: err = %v, want ErrInvalidCaveats", err)
	}
	issuer, _, _, _ := buildIssuerVerifier(t)
	_, _, err := issuer.Issue(context.Background(), IssueRequest{
		IssuedBy: Principal{Subject: "op"}, Subject: Principal{TenantID: uuid.New(), Subject: "a"},
		Audience: []string{audienceData}, Caveats: Caveats{Ops: []Op{OpGet}}, Generation: MaxIJSONInteger + 1,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("a generation past 2^53-1: err = %v, want ErrInvalidRequest", err)
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	hdr := map[string]any{"alg": algEd25519, "kid": "k1", "typ": TokenType}
	for name, mod := range map[string]func(map[string]any){
		"exp":          func(c map[string]any) { c["exp"] = int64(MaxIJSONInteger) + 1 },
		"limes_gen":    func(c map[string]any) { c["limes_gen"] = int64(MaxIJSONInteger) + 1 },
		"max_requests": func(c map[string]any) { detail(c)["max_requests"] = int64(MaxIJSONInteger) + 1 },
	} {
		claims := validClaims(uuid.New())
		mod(claims)
		if _, err := v.Verify(context.Background(), VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: audienceData}); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s past 2^53-1: err = %v, want ErrInvalidSignature", name, err)
		}
	}
}

// RFC 7493 §2.1: a token's JSON is UTF-8; one that is not is refused.
func TestTokenClaimsAreUTF8(t *testing.T) {
	if _, err := parseClaims(b64([]byte("{\"iss\":\"\xff\"}"))); err == nil {
		t.Error("claims with invalid UTF-8 were read")
	}
}
