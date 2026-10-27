package dpop

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oleg-tkachuk/limes/internal/jose"
)

const (
	// tokenEndpoint is an authorization server's token endpoint, and
	// tokenClient the client it authenticated.
	tokenEndpoint = "https://as.example.com/token"
	tokenClient   = "client-1"
	// presentedToken is the access token a resource request presents.
	presentedToken = "the-token"
)

// tokenProof is a proof key signs for a POST to url at, presenting token,
// none for a token request.
func tokenProof(t *testing.T, key crypto.Signer, url, token string, at time.Time) string {
	t.Helper()
	p, err := Sign(context.Background(), key, SignRequest{Method: dpopMethod, URL: url, Token: token, IssuedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A token endpoint accepts a proof by any key, presenting no token, and
// learns the key's thumbprint to bind what it issues to; the proof is
// recorded once, like any other.
func TestCheckTokenRequestBindsToTheProofKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, key := range dpopKeys(t) {
		t.Run(name, func(t *testing.T) {
			v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
			req := TokenRequest{Proof: Proof{JWT: tokenProof(t, key, tokenEndpoint, "", now), Method: dpopMethod, URL: tokenEndpoint}, Holder: tokenClient}
			checked, err := v.CheckTokenRequest(context.Background(), req)
			if err != nil {
				t.Fatalf("a token request's proof refused: %v", err)
			}
			if got, want := checked.JKT(), boundJKT(t, key); got != want {
				t.Errorf("JKT = %q, want the proof key's %q", got, want)
			}
			if err := v.Record(context.Background(), checked); err != nil {
				t.Fatal(err)
			}
			again, err := v.CheckTokenRequest(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Record(context.Background(), again); !errors.Is(err, ErrReplayed) {
				t.Errorf("a replayed token request's proof: %v, want ErrReplayed", err)
			}
		})
	}
}

// A token request's proof passes every check a resource request's does but
// the key and the token, and names no token.
func TestCheckTokenRequestRefusals(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	cases := map[string]struct {
		proof, url string
		want       error
	}{
		"no proof":           {"", tokenEndpoint, ErrRequired},
		"naming a token":     {tokenProof(t, key, tokenEndpoint, presentedToken, now), tokenEndpoint, ErrInvalid},
		"for another URL":    {tokenProof(t, key, dpopURL, "", now), tokenEndpoint, ErrInvalid},
		"stale":              {tokenProof(t, key, tokenEndpoint, "", now.Add(-2*time.Minute)), tokenEndpoint, ErrInvalid},
		"tampered signature": {tokenProof(t, key, tokenEndpoint, "", now) + "AA", tokenEndpoint, ErrInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
			_, err := v.CheckTokenRequest(context.Background(), TokenRequest{Proof: Proof{JWT: tc.proof, Method: dpopMethod, URL: tc.url}, Holder: tokenClient})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}

	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	proof := Proof{JWT: tokenProof(t, key, tokenEndpoint, "", now), Method: dpopMethod, URL: tokenEndpoint}
	if _, err := v.CheckTokenRequest(context.Background(), TokenRequest{Proof: proof}); err == nil {
		t.Error("a token request with no holder was checked")
	}
}

// A token endpoint with nonces asks for one as a resource server does.
func TestCheckTokenRequestRequiresTheServerNonce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	nonces := newHMACNonces(t, now)
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }, Nonces: nonces})
	proof := Proof{JWT: tokenProof(t, key, tokenEndpoint, "", now), Method: dpopMethod, URL: tokenEndpoint}
	if _, err := v.CheckTokenRequest(context.Background(), TokenRequest{Proof: proof, Holder: tokenClient}); !errors.Is(err, ErrNonceRequired) {
		t.Errorf("a token request's proof with no nonce: %v, want ErrNonceRequired", err)
	}
}

// A resource request is not a token request: a proof naming no token does
// not pass for one presenting a token, and a request presenting none is
// the caller's error, refused before the proof is read.
func TestCheckWantsATokenAndAProofOverIt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	v := newVerifier(t, VerifierConfig{Replay: newReplay(t, MemoryReplayCacheConfig{}), Now: func() time.Time { return now }})
	tokenless := Proof{JWT: tokenProof(t, key, dpopURL, "", now), Method: dpopMethod, URL: dpopURL}
	if _, err := v.Check(context.Background(), Request{Proof: tokenless, Token: presentedToken, JKT: boundJKT(t, key), Holder: testHolder}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a proof naming no token, for a request presenting one: %v, want ErrInvalid", err)
	}
	// No token is the caller's mistake, as no holder is, not the client's:
	// it is no ErrInvalid, which would answer the client with a 401.
	if _, err := v.Check(context.Background(), Request{Proof: tokenless, JKT: boundJKT(t, key), Holder: testHolder}); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("a request presenting no token: %v, want the caller's error", err)
	}
}

// Sign writes an ath only over a token: a token request's proof has none.
func TestSignWritesAthOnlyOverAToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	key := dpopKeys(t)["Ed25519"]
	for token, want := range map[string]bool{"": false, presentedToken: true} {
		parts, err := jose.Split(tokenProof(t, key, tokenEndpoint, token, now))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := jose.UnB64(parts.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var claims map[string]any
		if err := json.Unmarshal(raw, &claims); err != nil {
			t.Fatal(err)
		}
		if _, has := claims[claimATH]; has != want {
			t.Errorf("token %q: ath present = %v, want %v", token, has, want)
		}
	}
}

// claimATH is the proof claim holding the token's hash.
const claimATH = "ath"

// newHMACNonces is an HMACNonces on a fixed clock at now.
func newHMACNonces(t *testing.T, now time.Time) *HMACNonces {
	t.Helper()
	n, err := NewHMACNonces(HMACNoncesConfig{Keys: SealNonceKeys(nonceKey(1)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
