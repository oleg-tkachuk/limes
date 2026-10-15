package limes

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

// ─── verifier ──────────────────────────────────────────────────────────────

// resign builds a token from arbitrary header and claims, signed with priv.
func resign(t *testing.T, priv ed25519.PrivateKey, header map[string]any, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(input)))
}

func verifierFor(t *testing.T, pub ed25519.PublicKey, cfg VerifierConfig) *Verifier {
	t.Helper()
	cfg.Keys = staticKeys(t, map[string]ed25519.PublicKey{"k1": pub})
	cfg.Revocations = newMemStore()
	if cfg.TrustedIssuers == nil {
		cfg.TrustedIssuers = []string{"iss"}
	}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func validClaims(tenant uuid.UUID) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": "iss", "sub": "s", "aud": []string{"data"},
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "jti": uuid.NewString(),
		"limes_principal": map[string]any{"type": "agent", "tenant_id": tenant, "subject": "s"},
		"limes_caveats":   map[string]any{"ops": []string{"get"}},
	}
}

func TestVerifyRequiresCapabilityTyp(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	ctx := context.Background()

	good := resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}, validClaims(uuid.New()))
	if _, err := v.Verify(ctx, VerifyRequest{Token: good, Audience: "data"}); err != nil {
		t.Fatalf("well-formed token rejected: %v", err)
	}
	// Earlier formats, whose claims a v3 reader would read under another
	// schema: v1 (Go field names) and v2 (authorization_details, act).
	for _, typ := range []string{"JWT", "", "at+jwt", "limes-cap+jwt", "limes-cap-v2+jwt", "limes-cap-v3+jwt"} {
		tok := resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": typ}, validClaims(uuid.New()))
		if _, err := v.Verify(ctx, VerifyRequest{Token: tok, Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("typ %q: Verify = %v, want ErrInvalidToken", typ, err)
		}
	}
}

// A line break inside any segment refuses the token: the decoder would skip
// it, so a signature with one spliced in reads as the same signature and the
// altered string would verify as the original capability.
func TestVerifyRejectsLineBreaksInSegments(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	ctx := context.Background()
	good := resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}, validClaims(uuid.New()))
	if _, err := v.Verify(ctx, VerifyRequest{Token: good, Audience: "data"}); err != nil {
		t.Fatalf("well-formed token rejected: %v", err)
	}
	parts, err := jose.Split(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, brk := range []string{"\r", "\n", "\r\n"} {
		sig := parts.Signature[:4] + brk + parts.Signature[4:]
		tok := parts.SigningInput() + "." + sig
		if _, err := v.Verify(ctx, VerifyRequest{Token: tok, Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("signature with %q: Verify = %v, want ErrInvalidToken", brk, err)
		}
	}
}

func TestVerifyRejectsTenantlessAndOversizedTokens(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{MaxTokenBytes: 2048})
	ctx := context.Background()
	hdr := map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}

	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, validClaims(uuid.Nil)), Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("tenantless token: Verify = %v, want ErrInvalidToken", err)
	}

	big := validClaims(uuid.New())
	big["limes_caveats"] = map[string]any{"ops": []string{"get"}, "resource_uris": []string{strings.Repeat("x", 4096)}}
	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, big), Audience: "data"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversized token: Verify = %v, want a size rejection", err)
	}
}

func TestVerifyEnforcesKeyIssuerBinding(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{
		TrustedIssuers: []string{"iss", "other"},
		KeyIssuers:     map[string]string{"k1": "iss"},
	})
	// A trusted key the binding does not name signs for nobody.
	unbound := verifierFor(t, pub, VerifierConfig{
		TrustedIssuers: []string{"iss", "other"},
		KeyIssuers:     map[string]string{"k2": "iss"},
	})
	if _, err := unbound.Verify(context.Background(), VerifyRequest{
		Token: resign(t, priv, map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}, validClaims(uuid.New())), Audience: "data",
	}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a kid the binding does not name: Verify = %v, want ErrInvalidToken", err)
	}
	ctx := context.Background()
	hdr := map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}

	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, validClaims(uuid.New())), Audience: "data"}); err != nil {
		t.Fatalf("bound issuer rejected: %v", err)
	}
	claims := validClaims(uuid.New())
	claims["iss"] = "other" // trusted, but not for this key
	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("cross-issuer signature: Verify = %v, want ErrInvalidToken", err)
	}
}

// The claims are read strictly: a signed token that some other parser could
// read another way is refused, whoever signed it.
func TestVerifyReadsClaimsStrictly(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	hdr := map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}
	ctx := context.Background()
	cases := map[string]func(map[string]any){
		// A caveat this verifier does not know is one it cannot enforce.
		"unknown caveat": func(c map[string]any) { c["limes_caveats"].(map[string]any)["max_tokens"] = 5 },
		"unknown claim":  func(c map[string]any) { c["limes_scope"] = "admin" },
		"name in another case": func(c map[string]any) {
			c["limes_caveats"] = map[string]any{"OPS": []string{"get"}}
		},
		"the previous format": func(c map[string]any) {
			c["limes_caveats"] = map[string]any{"Ops": []string{"get"}}
		},
		"sub not the principal's": func(c map[string]any) { c["sub"] = "someone-else" },
		"amount as a number": func(c map[string]any) {
			c["limes_caveats"].(map[string]any)["max_budget"] = 1.5
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			claims := validClaims(uuid.New())
			mod(claims)
			if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

// A duplicate member is refused: encoding/json would keep the last, another
// parser the first.
func TestVerifyRefusesDuplicateClaims(t *testing.T) {
	_, err := parseClaims(jose.B64([]byte(`{"iss":"a","iss":"b"}`)))
	if err == nil {
		t.Fatal("claims with iss twice were read")
	}
}

// A verifier keeps the trust it was built with: changing the slices and maps
// of its config afterwards changes nothing it accepts.
func TestVerifierConfigIsItsOwn(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	cfg := VerifierConfig{
		TrustedIssuers:   []string{"iss", "other"},
		KeyIssuers:       map[string]string{"k1": "iss"},
		AcceptTokenTypes: []TokenType{TokenTypeV4},
	}
	v := verifierFor(t, pub, cfg)
	cfg.TrustedIssuers[0] = "scribbled"
	cfg.KeyIssuers["k1"] = "other"
	cfg.AcceptTokenTypes[0] = "scribbled"

	ctx := context.Background()
	hdr := map[string]any{"alg": jose.AlgEd25519, "kid": "k1", "typ": TokenTypeV4}
	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, validClaims(uuid.New())), Audience: "data"}); err != nil {
		t.Errorf("a token the verifier was built to accept: %v", err)
	}
	claims := validClaims(uuid.New())
	claims["iss"] = "other"
	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: "data"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token the config now binds k1 to: err = %v, want ErrInvalidToken", err)
	}
}
