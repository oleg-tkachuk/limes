package dpop_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/oleg-tkachuk/limes/dpop"
)

// The request every example signs a proof for, and the token it presents.
const (
	exampleMethod = "POST"
	exampleURL    = "https://data.example.com/objects/get"
	exampleToken  = "access-token"
	exampleHolder = "capability-1"
)

func ExampleSign() {
	ctx := context.Background()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	jkt, err := dpop.Thumbprint(key.Public())
	if err != nil {
		log.Fatal(err)
	}
	replay, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
	if err != nil {
		log.Fatal(err)
	}

	// The client signs one proof per request, over its method, URL and token.
	proof, err := dpop.Sign(ctx, key, dpop.SignRequest{
		Method: exampleMethod, URL: exampleURL, Token: exampleToken, IssuedAt: time.Now(),
	})
	if err != nil {
		log.Fatal(err)
	}
	req := dpop.Request{
		Proof:  dpop.Proof{JWT: proof, Method: exampleMethod, URL: exampleURL},
		Token:  exampleToken,
		JKT:    jkt,
		Holder: exampleHolder,
	}
	fmt.Println(verifier.Verify(ctx, req))
	fmt.Println(errors.Is(verifier.Verify(ctx, req), dpop.ErrReplayed))

	// A proof for another URL is no proof for this one.
	req.Proof.URL = "https://data.example.com/objects/delete"
	fmt.Println(errors.Is(verifier.Verify(ctx, req), dpop.ErrInvalid))
	// Output:
	// <nil>
	// true
	// true
}

func ExampleVerifier_Check() {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	jkt, _ := dpop.Thumbprint(key.Public())
	replay, _ := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	verifier, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
	if err != nil {
		log.Fatal(err)
	}
	proof, _ := dpop.Sign(ctx, key, dpop.SignRequest{
		Method: exampleMethod, URL: exampleURL, Token: exampleToken, IssuedAt: time.Now(),
	})
	req := dpop.Request{
		Proof: dpop.Proof{JWT: proof, Method: exampleMethod, URL: exampleURL},
		Token: exampleToken, JKT: jkt, Holder: exampleHolder,
	}

	// Check first, then the caller's own checks, and Record only once they
	// pass: a refused request spends no room in the replay cache.
	checked, err := verifier.Check(ctx, req)
	fmt.Println(err)
	tokenRevoked := false
	if !tokenRevoked {
		fmt.Println(verifier.Record(ctx, checked))
	}
	fmt.Println(errors.Is(verifier.Record(ctx, checked), dpop.ErrReplayed))
	// Output:
	// <nil>
	// <nil>
	// true
}

func ExampleNewHMACNonces() {
	ctx := context.Background()
	key := make([]byte, dpop.HMACNonceKeyBytes)
	if _, err := rand.Read(key); err != nil {
		log.Fatal(err)
	}
	// Every replica holding the same keys accepts the others' nonces.
	nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: dpop.SealNonceKeys(key)})
	if err != nil {
		log.Fatal(err)
	}
	nonce, err := nonces.Nonce(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(nonces.Valid(ctx, nonce))
	fmt.Println(nonces.Valid(ctx, "made-up"))
	// Output:
	// true <nil>
	// false <nil>
}

func ExampleVerifier_Nonce() {
	ctx := context.Background()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	jkt, _ := dpop.Thumbprint(key.Public())
	nonceKey := make([]byte, dpop.HMACNonceKeyBytes)
	if _, err := rand.Read(nonceKey); err != nil {
		log.Fatal(err)
	}
	nonces, _ := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: dpop.SealNonceKeys(nonceKey)})
	replay, _ := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	verifier, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Nonces: nonces})
	if err != nil {
		log.Fatal(err)
	}

	sign := func(nonce string) dpop.Request {
		proof, _ := dpop.Sign(ctx, key, dpop.SignRequest{
			Method: exampleMethod, URL: exampleURL, Token: exampleToken, Nonce: nonce, IssuedAt: time.Now(),
		})
		return dpop.Request{
			Proof: dpop.Proof{JWT: proof, Method: exampleMethod, URL: exampleURL},
			Token: exampleToken, JKT: jkt, Holder: exampleHolder,
		}
	}
	// A proof without the server's nonce is refused; the server sends one
	// in the DPoP-Nonce header, and the client signs again with it.
	fmt.Println(errors.Is(verifier.Verify(ctx, sign("")), dpop.ErrNonceRequired))
	nonce, err := verifier.Nonce(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(verifier.Verify(ctx, sign(nonce)))
	// Output:
	// true
	// <nil>
}

func ExampleNewMemoryReplayCache() {
	ctx := context.Background()
	cache, err := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
	if err != nil {
		log.Fatal(err)
	}
	entry := dpop.ReplayEntry{JKT: "jkt", JTI: "proof-1", Holder: exampleHolder, ExpiresAt: time.Now().Add(time.Minute)}
	fmt.Println(cache.Seen(ctx, entry))
	fmt.Println(cache.Seen(ctx, entry))
	// Output:
	// false <nil>
	// true <nil>
}

func ExampleThumbprint() {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	jkt, err := dpop.Thumbprint(key.Public())
	fmt.Println(err, dpop.IsThumbprint(jkt), dpop.IsThumbprint("not-a-thumbprint"))
	// Output:
	// <nil> true false
}

func ExampleSigningAlgorithms() {
	// What a verifier advertises in its DPoP challenge.
	fmt.Println(dpop.SigningAlgorithms())
	// Output:
	// [Ed25519 ES256]
}

func ExampleSealNonceKeys() {
	current := make([]byte, dpop.HMACNonceKeyBytes)
	previous := make([]byte, dpop.HMACNonceKeyBytes)
	// The first key signs; the rest still verify, for a rotation.
	keys := dpop.SealNonceKeys(current, previous)
	fmt.Println(keys.Len())
	fmt.Printf("%v %#v\n", keys, keys)
	// Output:
	// 2
	// [2 redacted] [2 redacted]
}
