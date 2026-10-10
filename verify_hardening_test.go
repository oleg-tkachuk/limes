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

func verifierFor(t *testing.T, pub ed25519.PublicKey, cfg VerifierConfig) *StandardVerifier {
	t.Helper()
	cfg.Keys = NewStaticKeyResolver(map[string]ed25519.PublicKey{"k1": pub})
	cfg.Revocations = NewCachedRevocationChecker(newMemStore(), -1)
	if cfg.TrustedIssuers == nil {
		cfg.TrustedIssuers = []string{"iss"}
	}
	v, err := NewStandardVerifier(cfg)
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
		"limes_principal": map[string]any{"tenant_id": tenant, "subject": "s"},
		"limes_caveats":   map[string]any{"ops": []string{"get"}},
	}
}

func TestVerifyRequiresCapabilityTyp(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	ctx := context.Background()

	good := resign(t, priv, map[string]any{"alg": "EdDSA", "kid": "k1", "typ": TokenType}, validClaims(uuid.New()))
	if _, err := v.Verify(ctx, VerifyRequest{Token: good, Audience: "data"}); err != nil {
		t.Fatalf("well-formed token rejected: %v", err)
	}
	// limes-cap+jwt is the previous format, read under another schema.
	for _, typ := range []string{"JWT", "", "at+jwt", "limes-cap+jwt"} {
		tok := resign(t, priv, map[string]any{"alg": "EdDSA", "kid": "k1", "typ": typ}, validClaims(uuid.New()))
		if _, err := v.Verify(ctx, VerifyRequest{Token: tok, Audience: "data"}); !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("typ %q: Verify = %v, want ErrInvalidSignature", typ, err)
		}
	}
}

func TestVerifyRejectsTenantlessAndOversizedTokens(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{MaxTokenBytes: 2048})
	ctx := context.Background()
	hdr := map[string]any{"alg": "EdDSA", "kid": "k1", "typ": TokenType}

	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, validClaims(uuid.Nil)), Audience: "data"}); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("tenantless token: Verify = %v, want ErrInvalidSignature", err)
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
	ctx := context.Background()
	hdr := map[string]any{"alg": "EdDSA", "kid": "k1", "typ": TokenType}

	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, validClaims(uuid.New())), Audience: "data"}); err != nil {
		t.Fatalf("bound issuer rejected: %v", err)
	}
	claims := validClaims(uuid.New())
	claims["iss"] = "other" // trusted, but not for this key
	if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: "data"}); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("cross-issuer signature: Verify = %v, want ErrInvalidSignature", err)
	}
}

// The claims are read strictly: a signed token that some other parser could
// read another way is refused, whoever signed it.
func TestVerifyReadsClaimsStrictly(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := verifierFor(t, pub, VerifierConfig{})
	hdr := map[string]any{"alg": "EdDSA", "kid": "k1", "typ": TokenType}
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
			if _, err := v.Verify(ctx, VerifyRequest{Token: resign(t, priv, hdr, claims), Audience: "data"}); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("err = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

// A duplicate member is refused: encoding/json would keep the last, another
// parser the first.
func TestVerifyRefusesDuplicateClaims(t *testing.T) {
	_, err := parseClaims(b64([]byte(`{"iss":"a","iss":"b"}`)))
	if err == nil {
		t.Fatal("claims with iss twice were read")
	}
}
